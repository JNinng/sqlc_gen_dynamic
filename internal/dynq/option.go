package dynq

import "fmt"

// QueryOption 是运行期能力收窄选项:对生成期白名单的纯减法投影——只能禁用、
// 不能开启。禁用为集合语义(幂等、可交换、去重);有效能力 = 生成期白名单 − 禁用集,
// 四个消费点(Parse / List / Count / Schema)同参。
type QueryOption func(*optionState)

type optionState struct {
	whereDisabled  []FieldName
	sortDisabled   []FieldName
	selectDisabled []FieldName
}

// DisableWhere 禁用字段的 where 能力(本调用点不可过滤)。
func DisableWhere(fields ...FieldName) QueryOption {
	return func(s *optionState) { s.whereDisabled = append(s.whereDisabled, fields...) }
}

// DisableSort 禁用字段的 sort 能力(本调用点不可排序)。
func DisableSort(fields ...FieldName) QueryOption {
	return func(s *optionState) { s.sortDisabled = append(s.sortDisabled, fields...) }
}

// DisableSelect 禁用字段的 select 能力(本调用点不可点名投影)。
func DisableSelect(fields ...FieldName) QueryOption {
	return func(s *optionState) { s.selectDisabled = append(s.selectDisabled, fields...) }
}

// QueryConfig 是 opts 应用于某表 registry 后的有效能力视图。
// 禁用目标不在 registry(含跨表误用)→ error 显式暴露编程错误——
// 禁用是权限收窄手段,静默 no-op 会制造安全假象。
type QueryConfig struct {
	meta           TableMeta
	whereDisabled  map[FieldName]struct{}
	sortDisabled   map[FieldName]struct{}
	selectDisabled map[FieldName]struct{}
}

// NewQueryConfig 应用 opts 并校验全部禁用目标。
func NewQueryConfig(meta TableMeta, opts ...QueryOption) (*QueryConfig, error) {
	st := &optionState{}
	for _, opt := range opts {
		if opt != nil {
			opt(st)
		}
	}
	cfg := &QueryConfig{
		meta:           meta,
		whereDisabled:  toSet(st.whereDisabled),
		sortDisabled:   toSet(st.sortDisabled),
		selectDisabled: toSet(st.selectDisabled),
	}
	if err := cfg.ValidateTargets(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func toSet(fields []FieldName) map[FieldName]struct{} {
	if len(fields) == 0 {
		return nil
	}
	set := make(map[FieldName]struct{}, len(fields))
	for _, f := range fields {
		set[f] = struct{}{}
	}
	return set
}

// WhereDisabled 查询字段 where 域是否被禁用。
func (c *QueryConfig) WhereDisabled(key FieldName) bool {
	_, ok := c.whereDisabled[key]
	return ok
}

// SortDisabled 查询字段 sort 域是否被禁用。
func (c *QueryConfig) SortDisabled(key FieldName) bool {
	_, ok := c.sortDisabled[key]
	return ok
}

// SelectDisabled 查询字段 select 域是否被禁用。
func (c *QueryConfig) SelectDisabled(key FieldName) bool {
	_, ok := c.selectDisabled[key]
	return ok
}

// Meta 返回关联的 registry。
func (c *QueryConfig) Meta() TableMeta { return c.meta }

// ValidateTargets 校验禁用目标全部在本表 registry 中(含未知与跨表误用)。
// NewQueryConfig 构造时已调用;导出以供需要延迟校验的场景复用。
func (c *QueryConfig) ValidateTargets() error {
	for domain, set := range map[string]map[FieldName]struct{}{
		"where": c.whereDisabled, "sort": c.sortDisabled, "select": c.selectDisabled,
	} {
		for f := range set {
			if _, ok := c.meta.Field(f); !ok {
				return NewErr(KindOption, string(f),
					domain+" disable target not in "+c.meta.Resource+" registry (unknown or cross-table field)", -1)
			}
		}
	}
	return nil
}

// Slots 是协议门的条件冲突检测器:任意两个条件翻译后命中同一 Params 目标字段
// (含 between 拆分出的 Gte/Lte 与独立 gte/lte 的重叠、同 field+op 重复出现)
// → conflicting conditions,定位到后一条下标。不存在"后写覆盖先写"路径。
type Slots struct {
	taken map[string]int
}

// NewSlots 构造冲突检测器。
func NewSlots() *Slots { return &Slots{taken: map[string]int{}} }

// Take 占用目标槽位;重复占用返回 conflicting conditions 错误。
func (s *Slots) Take(target string, index int, field FieldName) error {
	if prev, ok := s.taken[target]; ok {
		e := NewErr(KindWhere, string(field), "conflicting conditions", index)
		e.Reason = fmt.Sprintf("conflicting conditions (target %q already set by condition %d)", target, prev)
		return e
	}
	s.taken[target] = index
	return nil
}

// Budget 是占位符总预算:所有 in/not_in 数组长度之和 + 条数,解析期累加校验。
type Budget struct{ used int }

// NewBudget 构造预算计数器。
func NewBudget() *Budget { return &Budget{} }

// Add 累加 n 个占位符;超预算返回错误(400)。
func (b *Budget) Add(n int) error {
	b.used += n
	if b.used > PlaceholderBudget {
		return NewErr(KindWhere, "", "placeholder budget exceeded", -1)
	}
	return nil
}

// Used 返回已用量。
func (b *Budget) Used() int { return b.used }

// ErrTooManyConditions 构造 where 条数超限错误(生成代码引用,避免 import fmt)。
func ErrTooManyConditions() *Error {
	return NewErr(KindWhere, "", fmt.Sprintf("too many conditions (max %d)", MaxWhere), -1)
}
