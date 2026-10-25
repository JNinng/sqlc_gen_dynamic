package dynq

import (
	"fmt"

	sq "github.com/Masterminds/squirrel"
)

// 动态更新面(typed 门)的运行时基型:选项收集、SET / WHERE 槽位冲突检测与
// 零条件守卫。生成代码只提供表相关数据(列名、Go 类型、SQL 片段),
// 校验与拒绝语义全部收敛于此。
//
// 与 List 的 typed 门同一纪律:任何歧义或危险输入显式拒绝,不存在
// 静默覆盖、静默丢弃、静默 no-op 路径。

// UpdateOption 是动态更新选项的泛型基型:选项与表 marker 类型 T 绑定,
// 跨表误用(SET / WHERE 选项串表)在编译期被拒绝,而不是运行期报错。
type UpdateOption[T any] func(*UpdateState[T]) error

// SetAssign 是一条已收集的 SET 赋值。Cast 是占位符的显式 cast 后缀
// (如 "::uuid"),空 = 无。
type SetAssign struct {
	Column string
	Cast   string
	Value  any
}

// UpdateState 收集一次 UPDATE 的 SET 白名单赋值与调用方 WHERE 条件。
// 同一实例只对应一条 UPDATE 语句;任一选项返回错误时调用方必须放弃执行。
type UpdateState[T any] struct {
	sets         []SetAssign
	setSlots     *Slots
	whereSlots   *Slots
	wheres       []sq.Sqlizer
	bypassFilter bool
}

// NewUpdateState 构造一次 UPDATE 的选项收集器。
func NewUpdateState[T any]() *UpdateState[T] {
	return &UpdateState[T]{setSlots: NewSlots(), whereSlots: NewSlots()}
}

// Set 记录一条 SET 赋值(field 为逻辑键,column 为物理列名)。
// 同列重复 Set → KindSet 冲突,不存在"后写覆盖先写"。
func (s *UpdateState[T]) Set(field FieldName, column, cast string, value any) error {
	if err := s.setSlots.TakeSet(string(field), len(s.sets), field); err != nil {
		return err
	}
	s.sets = append(s.sets, SetAssign{Column: column, Cast: cast, Value: value})
	return nil
}

// Where 记录一条调用方定位条件。同一 (字段, op) 槽位重复 → KindWhere 冲突;
// between 拆占 gte / lte 双槽,与独立 gte / lte 的重叠天然被拦截。
// is_null / not_null 恒为互斥关系(与 List 一致),共用同一 null 槽位。
func (s *UpdateState[T]) Where(field FieldName, op Op, cond sq.Sqlizer) error {
	slotOp := string(op)
	if op == OpIsNull || op == OpNotNull {
		slotOp = "null"
	}
	target := string(field) + "." + slotOp
	if err := s.whereSlots.Take(target, len(s.wheres), field); err != nil {
		return err
	}
	s.wheres = append(s.wheres, cond)
	return nil
}

// Sets 返回按调用序收集的赋值。
func (s *UpdateState[T]) Sets() []SetAssign { return s.sets }

// Wheres 返回按调用序收集的调用方条件。
func (s *UpdateState[T]) Wheres() []sq.Sqlizer { return s.wheres }

// BypassFilter 置位 @filter 绕过标志(生成代码的 WithoutFilter 选项调用)。
// 只影响恒定的 @filter 不变量,不干预调用方条件与校验;幂等。
func (s *UpdateState[T]) BypassFilter() { s.bypassFilter = true }

// FilterBypassed 报告本次 UPDATE 是否绕过 @filter 不变量。
func (s *UpdateState[T]) FilterBypassed() bool { return s.bypassFilter }

// Validate 校验空 SET 与零 where 守卫,必须在执行 SQL 之前调用。
//
// 零 where 按"调用方条件数"计:@filter 谓词恒定叠加后最终 SQL 的 WHERE
// 恒非空,不豁免该守卫——不得以最终 SQL 形态代替判断,否则恒等不变量
// 会让全表 UPDATE 静默成立。@scope 不参与写面,不在此计数。
func (s *UpdateState[T]) Validate() error {
	if len(s.sets) == 0 {
		return NewErr(KindSet, "", "empty set: at least one column assignment is required", -1)
	}
	if len(s.wheres) == 0 {
		return NewErr(KindWhere, "", "zero where: a caller-supplied condition is required (no full-table update)", -1)
	}
	return nil
}

// CheckInLen 校验 typed 门 in / not_in 的数组长度:1 ≤ n ≤ MaxIn。
// 空数组无法构成合法 IN 列表,与协议门同一上限。
func CheckInLen(field FieldName, n int) error {
	if n == 0 {
		return NewErr(KindWhere, string(field), "in array must not be empty", -1)
	}
	if n > MaxIn {
		return NewErr(KindWhere, string(field), fmt.Sprintf("in array length %d exceeds MaxIn %d", n, MaxIn), -1)
	}
	return nil
}

// ErrBetweenOrder 构造 between 边界倒置错误(生成代码引用,避免 import fmt)。
func ErrBetweenOrder(field FieldName) *Error {
	return NewErr(KindWhere, string(field), "between lower bound exceeds upper bound", -1)
}

// ErrEmptyPattern 构造空串模糊定位值错误:空串会退化为 ILIKE '%%',命中
// 全部非 NULL 行,等价于无定位条件——静默批量更新,必须显式拒绝。
func ErrEmptyPattern(field FieldName) *Error {
	return NewErr(KindWhere, string(field), "empty pattern: contains/prefix/suffix must not be empty", -1)
}

// ErrEmptyJSON 构造空 jsonb 定位值错误:空切片会生成下列 SQL,由 PG 在运行期
// 报 invalid input syntax 错误而非返回空结果——与空串模糊定位守卫同层,
// 必须显式拒绝:
//
//	doc @> ''::jsonb
func ErrEmptyJSON(field FieldName) *Error {
	return NewErr(KindWhere, string(field), "empty json: has must carry a non-empty JSON document", -1)
}

// ErrNilSet 构造 NOT NULL 切片列(nil 会编码为 NULL)的赋值错误:签名只有
// T 没有 *T,nil 通道必须运行期拒绝,与"非空列无 nil 通道"承诺一致。
func ErrNilSet(field FieldName) *Error {
	return NewErr(KindSet, string(field), "nil value for NOT NULL column", -1)
}
