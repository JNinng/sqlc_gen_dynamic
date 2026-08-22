package dynq

import (
	"strings"

	sq "github.com/Masterminds/squirrel"
)

// SortField 是有效排序的一个列:物理列名来自生成期白名单,方向与 NULLS 方位
// 显式配对(ASC → NULLS LAST,DESC → NULLS FIRST,即 Postgres 默认方位,
// 显式写出以与 seek 模板一一对应、杜绝歧义)。
type SortField struct {
	Key      FieldName
	Column   string
	DataType DataType
	Nullable bool
	Desc     bool
	// Cast 是占位符的显式 cast 后缀(uuid/enum 列的文本参数需要),空 = 无。
	Cast string
}

// ResolveSort 解析有效排序(查询函数内统一执行,与游标指纹同源):
//  1. 用户排序非空 → 逐项校验 ∈ registry、已声明 @sortable、未被 opts 禁用。
//  2. 为空 → 回落表级 @default_sort。
//  3. 末列若非主键(@pk) → 无条件追加主键 ASC 作 tiebreaker,保证全序。
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

// CursorFingerprint 计算排序指纹:FNV-64(资源名 ‖ 有效排序全序列的逻辑列名+方向)。
// 掺入资源名使跨表同规格排序(如多表都有 created_at DESC, id DESC)的游标互不通用;
// 换排序、跨表、跨能力视图复用旧游标 → 指纹不符。指纹不含 page_size。
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

// SeekPredicate 构造游标定位谓词:定位"严格在游标行之后"的行。
//
//		P = ⋁ᵢ ( E(c₁,v₁) ∧ … ∧ E(cᵢ₋₁,vᵢ₋₁) ∧ Kᵢ(cᵢ,vᵢ) )
//
//	  - 空值安全等值 E(x,v) = x IS NOT DISTINCT FROM v:游标值本身为 NULL 时,
//	    普通 = 恒为 NULL 会导致漏行;中位列非空时退化为普通 =(语义等价,索引友好)。
//	  - 方向化严格比较 Kᵢ 按 NULLS 方位分情形:
//	    ASC(NULLS LAST):v 为 NULL → 整项模板期消除;v 非 NULL → x > v OR x IS NULL
//	    DESC(NULLS FIRST):v 为 NULL → x IS NOT NULL;v 非 NULL → x < v
//	  - 末列为主键(非空),K 退化为普通 > / <。
//	  - 全部 vi 经参数化占位符传入;列名与方向来自同一白名单的有效排序。
func SeekPredicate(seq []SortField, vals []any) (sq.Sqlizer, error) {
	if len(seq) == 0 {
		return nil, NewErr(KindCursor, "", "empty sort sequence", -1)
	}
	if len(vals) != len(seq) {
		return nil, NewErr(KindCursor, "", "cursor value count mismatch", -1)
	}
	// 末列为主键(非空),游标值为 NULL 仅可能来自篡改 → 显式空集。
	if vals[len(seq)-1] == nil {
		return sq.Expr("false"), nil
	}
	var groups []sq.Sqlizer
	for i := range seq {
		key := seekKey(seq, vals, i)
		if key == nil {
			continue // 模板期消除:ASC 游标值为 NULL,该组之后无行
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

// seekEq 构造第 j(< i)列的空值安全等值:可空列 IS NOT DISTINCT FROM,非空列普通 =。
func seekEq(f SortField, v any) sq.Sqlizer {
	if f.Nullable {
		return sq.Expr(f.Column+" IS NOT DISTINCT FROM ?"+f.Cast, v)
	}
	return sq.Expr(f.Column+" = ?"+f.Cast, v)
}

// seekKey 构造第 i 列的方向化严格比较;返回 nil 表示该项为 FALSE,模板期消除。
func seekKey(seq []SortField, vals []any, i int) sq.Sqlizer {
	col, v, desc := seq[i].Column, vals[i], seq[i].Desc
	cast := seq[i].Cast
	last := i == len(seq)-1
	isNull := v == nil
	switch {
	case last: // 主键非空,v 必非 NULL,退化为普通严格比较
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
