package dynq

import (
	"strings"

	sq "github.com/Masterminds/squirrel"
)

// SortField 描述有效排序中的一个列:列名来自生成期白名单;排序方向决定 NULL
// 值的先后(ASC 排最后、DESC 排最前,即 Postgres 默认),ORDER BY 与游标
// 定位条件据此保持一致。
type SortField struct {
	Key      FieldName
	Column   string
	DataType DataType
	Nullable bool
	Desc     bool
	// Cast 是占位符的显式 cast 后缀(uuid/enum 列的文本参数需要),空 = 无。
	Cast string
}

// ResolveSort 解析有效排序(查询函数内统一执行):
//  1. 用户指定排序 → 逐项校验字段在 registry 中、已声明 @sortable、未被 opts 禁用。
//  2. 未指定 → 回落表级 @default_sort。
//  3. 末列若非主键 → 追加主键 ASC 作 tiebreaker,保证排序全序(分页与游标定位都依赖)。
func ResolveSort(meta TableMeta, cfg *QueryConfig, order []OrderBy) ([]SortField, error) {
	var seq []SortField
	if len(order) > 0 {
		for i, ob := range order {
			f, ok := meta.Field(ob.Field)
			if !ok {
				return nil, NewErr(KindSort, string(ob.Field), "unknown field", i)
			}
			if f.Hidden {
				return nil, NewErr(KindSort, string(ob.Field), "field hidden", i)
			}
			if !f.Sortable {
				return nil, NewErr(KindSort, string(ob.Field), "field not sortable", i)
			}
			if cfg != nil && cfg.SortDisabled(f.Key) {
				return nil, NewErr(KindSort, string(ob.Field), "field disabled in this context", i)
			}
			seq = append(seq, sortFieldOf(f, ob.Desc))
		}
	} else {
		for _, ob := range meta.DefaultSort {
			if f, ok := meta.Field(ob.Field); ok {
				seq = append(seq, sortFieldOf(f, ob.Desc))
			}
		}
	}
	// tiebreaker:末列已为主键则不重复追加。
	if len(seq) == 0 || seq[len(seq)-1].Key != meta.Tiebreaker {
		seq = append(seq, sortFieldOf(meta.TiebreakerField(), false))
	}
	return seq, nil
}

func sortFieldOf(f FieldMeta, desc bool) SortField {
	return SortField{Key: f.Key, Column: f.Column, DataType: f.DataType, Nullable: f.Nullable, Desc: desc, Cast: f.Cast}
}

// OrderBySQL 生成 ORDER BY 子句正文(列名与方向全部来自白名单,无用户输入)。
func OrderBySQL(seq []SortField) string {
	parts := make([]string, 0, len(seq))
	for _, f := range seq {
		if f.Desc {
			parts = append(parts, f.Column+" DESC NULLS FIRST")
		} else {
			parts = append(parts, f.Column+" ASC NULLS LAST")
		}
	}
	return strings.Join(parts, ", ")
}

// CursorFingerprint 计算排序指纹(FNV-64 哈希),输入为资源名与有效排序各列的
// 逻辑列名和方向。掺入资源名,使跨表同规格排序(如两张表都是 created_at DESC、
// id DESC)的游标互不通用;换排序、跨表、跨能力视图复用旧游标 → 指纹不符报错。
// 指纹不含 page_size,调整每页条数不影响游标。
func CursorFingerprint(resource string, seq []SortField) uint64 {
	var b strings.Builder
	b.WriteString(resource)
	for _, f := range seq {
		b.WriteByte(0)
		b.WriteString(string(f.Key))
		if f.Desc {
			b.WriteByte('D')
		} else {
			b.WriteByte('A')
		}
	}
	return fnv64a(b.String())
}

// SeekPredicate 构造游标定位谓词:选出排序上严格位于游标行之后的所有行。
//
// 做法:把"在游标行之后"拆成互斥情形并取 OR——对每一列 i,前 i-1 列与游标
// 值相等,且第 i 列严格更靠后。具体规则:
//
//   - "相等"用 IS NOT DISTINCT FROM:游标值可能为 NULL,普通 = 遇到 NULL 恒为
//     假会漏行;非空列直接用普通 =。
//   - "严格更靠后"取决于排序方向:
//     ASC(NULL 排最后):游标值为 NULL 时其后无行,该情形省略;
//     否则为 列 > 值 OR 列 IS NULL。
//     DESC(NULL 排最前):游标值为 NULL 时为 列 IS NOT NULL;
//     否则为 列 < 值。
//   - 末列是主键(非空),游标值只可能非 NULL,直接用普通 > / <;为 NULL 只可能
//     来自篡改,返回显式 FALSE。
//   - 全部值经占位符参数化传入;列名与方向均来自白名单的有效排序。
//
// 示例:有效排序为 age ASC、id ASC(主键 tiebreaker),游标值 (30, 5),age 可空,
// 生成的条件为:
//
//	(age > ? OR age IS NULL) OR (age IS NOT DISTINCT FROM ? AND id > ?)
//
// 含义:age 已大于 30 的行(含 NULL——ASC 排序中 NULL 排最后,更靠后),或 age
// 恰为 30 且 id 大于 5 的行;二者并集即排序上位于 (30, 5) 之后的所有行。
//
// 游标值为 NULL 的示例:score DESC、id ASC,游标值 (NULL, 7),score 可空:
//
//	(score IS NOT NULL) OR (score IS NOT DISTINCT FROM ? AND id > ?)
//
// 第一项对应 DESC 排序中 NULL 排最前、其后的行必非 NULL。
func SeekPredicate(seq []SortField, vals []any) (sq.Sqlizer, error) {
	if len(seq) == 0 {
		return nil, NewErr(KindCursor, "", "empty sort sequence", -1)
	}
	if len(vals) != len(seq) {
		return nil, NewErr(KindCursor, "", "cursor value count mismatch", -1)
	}
	// 末列是主键且非空,游标值为 NULL 只可能来自篡改,直接返回空结果。
	if vals[len(seq)-1] == nil {
		return sq.Expr("false"), nil
	}
	var groups []sq.Sqlizer
	for i := range seq {
		key := seekKey(seq, vals, i)
		if key == nil {
			continue // ASC 排序下游标值为 NULL,其后无行,该情形不产生条件
		}
		parts := make([]sq.Sqlizer, 0, i+1)
		for j := 0; j < i; j++ {
			parts = append(parts, seekEq(seq[j], vals[j]))
		}
		parts = append(parts, key)
		groups = append(groups, sq.And(parts))
	}
	if len(groups) == 0 {
		return sq.Expr("false"), nil
	}
	if len(groups) == 1 {
		return groups[0], nil
	}
	return sq.Or(groups), nil
}

// seekEq 构造第 j 列"等于游标值"的条件:可空列用 IS NOT DISTINCT FROM,非空列用普通 =。
func seekEq(f SortField, v any) sq.Sqlizer {
	if f.Nullable {
		return sq.Expr(f.Column+" IS NOT DISTINCT FROM ?"+f.Cast, v)
	}
	return sq.Expr(f.Column+" = ?"+f.Cast, v)
}

// seekKey 构造第 i 列"严格更靠后"的条件;返回 nil 表示该情形不存在
// (ASC 排序下游标值为 NULL,其后无行)。
func seekKey(seq []SortField, vals []any, i int) sq.Sqlizer {
	col, v, desc := seq[i].Column, vals[i], seq[i].Desc
	cast := seq[i].Cast
	last := i == len(seq)-1
	isNull := v == nil
	switch {
	case last: // 末列是主键且非空,游标值必然非 NULL,直接用普通严格比较
		if isNull {
			return sq.Expr("false")
		}
		if desc {
			return sq.Expr(col+" < ?"+cast, v)
		}
		return sq.Expr(col+" > ?"+cast, v)
	case desc: // NULLS FIRST
		if isNull {
			return sq.Expr(col + " IS NOT NULL")
		}
		return sq.Expr(col+" < ?"+cast, v)
	default: // ASC, NULLS LAST
		if isNull {
			return nil // NULL 组之后无行,整项消除
		}
		return sq.Or([]sq.Sqlizer{sq.Expr(col+" > ?"+cast, v), sq.Expr(col + " IS NULL")})
	}
}
