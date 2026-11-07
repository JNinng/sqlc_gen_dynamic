package main

import (
	"fmt"
	"strconv"
	"strings"
)

// gen_update.go:动态更新面(Table marker / UpdateOption 别名 / Set 与 Where
// 方法族 / @filter 绕过 / UpdateXxx 执行函数)。
//
// 能力边界:
//   - SET 资格 = @updatable 的 set-capable 形态(auto / set / 显式清单);
//   - WHERE 资格 = @updatable 的 where-capable 形态(auto / where / 显式清单),
//     有 @pk 时另自动附带主键列的完整类型矩阵(定位方法,永不生成 Set);
//   - 与 @dynamic(读面过滤能力)、@scope(读侧数据权限)、@internal(协议
//     可见性)完全解耦;
//   - 无 @pk 表只生成更新面:header + marker + Set / Where + UpdateXxx,
//     不生成 registry / Params / Parse / List / Count / Schema / 游标。

// updateOpSuffix 是 op → Where 方法名后缀。与 opSuffix 不同,eq 也显式带
// Eq:方法名本身承载定位语义(WhereIDEq),而非 Params 的裸字段名槽位。
var updateOpSuffix = map[string]string{
	"eq": "Eq", "neq": "Neq", "gt": "Gt", "gte": "Gte", "lt": "Lt", "lte": "Lte",
	"between": "Between", "in": "In", "not_in": "NotIn",
	"contains": "Contains", "prefix": "Prefix", "suffix": "Suffix",
	"is_null": "IsNull", "not_null": "NotNull", "has": "Has",
}

// updateOpSQL 是 Where 方法注释中的 SQL 形态描述。
var updateOpSQL = map[string]string{
	"eq": "=", "neq": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
	"between": "between", "in": "IN", "not_in": "NOT IN",
	"contains": "ILIKE 包含", "prefix": "ILIKE 前缀", "suffix": "ILIKE 后缀",
	"is_null": "IS NULL", "not_null": "IS NOT NULL", "has": "(col::jsonb) @>",
}

func (tc *tableCode) updateSection() string {
	if !tc.spec.UpdateEnabled {
		return ""
	}
	var p printer
	p.Raw(tc.updateMarkerSection())
	p.Raw(tc.updateSetSection())
	p.Raw(tc.updateWithoutFilterSection())
	p.Raw(tc.updateWhereSection())
	p.Raw(tc.updateFuncSection())
	p.Raw(tc.updateReturningSection())
	return p.String()
}

// ---- Table marker 与选项别名 ----

func (tc *tableCode) updateMarkerSection() string {
	var p printer
	p.Raw(fill(updateMarkerSkel, "sing", tc.spec.Singular))
	return p.String()
}

// ---- SET 方法族 ----

func (tc *tableCode) updateSetSection() string {
	sing := tc.spec.Singular
	var p printer
	p.Raw(updateSetHead)
	for _, c := range tc.spec.Updatables {
		typ := c.GoType
		if c.Nullable {
			typ = "*" + c.GoType
		}
		nilTail := "，无 nil"
		if c.Nullable {
			nilTail = "，nil = 显式置 NULL，字面量取指针用 dynq.Ptr"
		}
		if label := setCommentLabel(c); label != "" {
			p.Linef("// Set%s 设置 %s（%s）列值%s。", c.GoConst, c.Key, label, nilTail)
		} else {
			p.Linef("// Set%s 设置 %s 列值%s。", c.GoConst, c.Key, nilTail)
		}
		p.Linef("func (%sTable) Set%s(v %s) %sUpdateOption {", sing, c.GoConst, typ, sing)
		p.Linef("\treturn func(st *dynq.UpdateState[%sTable]) error {", sing)
		if !c.Nullable && strings.HasPrefix(c.GoType, "[]") {
			// 切片类型的 nil 会编码为 NULL;非空列必须运行期拒绝,
			// 使"非空列无 nil 通道"的签名承诺对切片同样成立。
			p.Linef("\t\tif v == nil {")
			p.Linef("\t\t\treturn dynq.ErrNilSet(%q)", c.Key)
			p.Line("\t\t}")
		}
		p.Linef("\t\treturn st.Set(%q, %q, %q, v)", c.Key, c.SQLName, c.whereCast())
		p.Line("\t}")
		p.Line("}")
		p.Blank()
	}
	return p.String()
}

// setCommentLabel 提取注释的非指令部分(即列 label,如"姓名"):折叠空白,
// 缺省 label(等于物理列名)不重复输出。
func setCommentLabel(c *ColumnSpec) string {
	label := strings.Join(strings.Fields(c.Label), " ")
	if label == "" || label == c.Physical {
		return ""
	}
	return label
}

// ---- @filter 绕过 ----

func (tc *tableCode) updateWithoutFilterSection() string {
	if len(tc.spec.Filters) == 0 {
		return ""
	}
	var p printer
	p.Raw(fill(withoutFilterSkel, "sing", tc.spec.Singular))
	return p.String()
}

// ---- WHERE 定位方法族 ----

// updateWhereField 是一个字段的 WHERE 方法生成单元。
type updateWhereField struct {
	col *ColumnSpec
	ops []string
}

// updateWhereFields 返回 WHERE 方法族的字段与 op 清单:
//   - @pk 列自动获得完整类型矩阵(有 @pk 即自动生成定位方法,永不生成 Set);
//   - 其余列按 where-capable @updatable 的 UpdateOps;
//   - 同列重复 @updatable 已在 mergeTags 取首条。
func (tc *tableCode) updateWhereFields() []updateWhereField {
	var out []updateWhereField
	for i := range tc.spec.Columns {
		c := &tc.spec.Columns[i]
		if tc.spec.ReadEnabled && c.Key == tc.spec.Tiebreaker {
			out = append(out, updateWhereField{col: c, ops: matrixOps(c.DataType)})
			continue
		}
		if c.WhereCapable {
			out = append(out, updateWhereField{col: c, ops: c.UpdateOps})
		}
	}
	return out
}

func (tc *tableCode) updateWhereSection() string {
	sing := tc.spec.Singular
	var p printer
	p.Raw(updateWhereHead)
	for _, f := range tc.updateWhereFields() {
		for _, op := range f.ops {
			name := "Where" + f.col.GoConst + updateOpSuffix[op]
			sqlDesc := updateOpSQL[op]
			if op == "in" && f.col.DataType == "int_array" {
				// 数组列的 in 是 && 重叠语义,注释不能写 IN。
				sqlDesc = "&& 重叠"
			}
			p.Linef("// %s 追加 %s 列 %s 条件;多条 WHERE 之间恒为 AND。", name, f.col.Key, sqlDesc)
			p.Linef("func (%sTable) %s(%s) %sUpdateOption {", sing, name, updateWhereSignature(f.col, op), sing)
			p.Linef("\treturn func(st *dynq.UpdateState[%sTable]) error {", sing)
			p.Indented("\t\t", updateWhereBody(f.col, op))
			p.Line("\t}")
			p.Line("}")
			p.Blank()
		}
	}
	return p.String()
}

// updateWhereSignature 按 op 元数生成参数列表:
// 单值 / 双值(between)/ 数组(in 系)/ 无值(is_null 系)。
func updateWhereSignature(c *ColumnSpec, op string) string {
	switch op {
	case "eq", "neq", "gt", "gte", "lt", "lte":
		return "v " + c.GoType
	case "between":
		return "lo, hi " + c.GoType
	case "in", "not_in":
		if c.DataType == "int_array" {
			return "vs " + c.GoType // 数组列:单数组 && 重叠,不再包一层
		}
		return "vs ..." + c.GoType
	case "contains", "prefix", "suffix":
		return "v string"
	case "has":
		return "v []byte"
	default: // is_null / not_null
		return ""
	}
}

// updateWhereBody 生成单个 (列, op) 的选项函数体:条件 SQL 内联(列名与
// 操作符为生成期白名单字面量),槽位冲突与零 where 守卫由 UpdateState 负责。
func updateWhereBody(c *ColumnSpec, op string) string {
	key := fmt.Sprintf("%q", c.Key)
	col := c.SQLName
	cast := c.whereCast()
	switch op {
	case "eq", "neq", "gt", "gte", "lt", "lte":
		sqlop := map[string]string{"eq": "=", "neq": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[op]
		return fmt.Sprintf("return st.Where(%s, %s, sq.Expr(%q, v))",
			key, opConst[op], col+" "+sqlop+" ?"+cast)
	case "between":
		// lo ≤ hi 运行期校验:倒置区间恒空集,静默执行等于 no-op,必须显式拒绝。
		cmp := "lo > hi"
		if c.DataType == "datetime" || c.DataType == "date" {
			cmp = "lo.After(hi)"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "if %s {\n\treturn dynq.ErrBetweenOrder(%s)\n}\n", cmp, key)
		fmt.Fprintf(&b, "if err := st.Where(%s, dynq.OpGte, sq.Expr(%q, lo)); err != nil {\n\treturn err\n}\n",
			key, col+" >= ?"+cast)
		fmt.Fprintf(&b, "return st.Where(%s, dynq.OpLte, sq.Expr(%q, hi))", key, col+" <= ?"+cast)
		return b.String()
	case "in", "not_in":
		var b strings.Builder
		fmt.Fprintf(&b, "if err := dynq.CheckInLen(%s, len(vs)); err != nil {\n\treturn err\n}\n", key)
		if c.DataType == "int_array" {
			// in(int_array 列)= && 数组重叠语义(与 List 同源)。
			fmt.Fprintf(&b, "return st.Where(%s, %s, sq.Expr(%q, vs))", key, opConst[op], col+" && ?"+cast)
			return b.String()
		}
		inOp := "IN"
		if op == "not_in" {
			inOp = "NOT IN"
		}
		placeholders := "dynq.Placeholders(len(vs))"
		if cast != "" {
			placeholders = fmt.Sprintf("dynq.PlaceholdersCast(len(vs), %q)", cast)
		}
		fmt.Fprintf(&b, "return st.Where(%s, %s, sq.Expr(%q+%s+%q, dynq.Anys(vs)...))",
			key, opConst[op], col+" "+inOp+" (", placeholders, ")")
		return b.String()
	case "contains", "prefix", "suffix":
		pattern := map[string]string{
			"contains": "dynq.ContainsPattern", "prefix": "dynq.PrefixPattern", "suffix": "dynq.SuffixPattern",
		}[op]
		// 空串会退化为 ILIKE '%%'(命中全部非 NULL 行),等同无定位条件;
		// 必须显式拒绝,否则零 where 守卫被静默绕过。
		var b strings.Builder
		fmt.Fprintf(&b, "if v == \"\" {\n\treturn dynq.ErrEmptyPattern(%s)\n}\n", key)
		fmt.Fprintf(&b, "return st.Where(%s, %s, sq.Expr(%q, %s(v)))",
			key, opConst[op], col+" ILIKE ? ESCAPE '\\'", pattern)
		return b.String()
	case "is_null":
		return fmt.Sprintf("return st.Where(%s, dynq.OpIsNull, sq.Expr(%q))", key, col+" IS NULL")
	case "not_null":
		return fmt.Sprintf("return st.Where(%s, dynq.OpNotNull, sq.Expr(%q))", key, col+" IS NOT NULL")
	case "has":
		// 空 jsonb 定位值会生成 @> ''::jsonb,由 PG 在运行期报语法错误;与空串
		// 模糊定位守卫同层,必须显式拒绝。左列 cast 到 jsonb:json 列没有
		// json @> jsonb 运算符,(col::jsonb) 对 jsonb 列是 no-op,两型共用一条形态。
		var b strings.Builder
		fmt.Fprintf(&b, "if len(v) == 0 {\n\treturn dynq.ErrEmptyJSON(%s)\n}\n", key)
		fmt.Fprintf(&b, "return st.Where(%s, dynq.OpHas, sq.Expr(%q, string(v)))", key, "("+col+"::jsonb) @> ?::jsonb")
		return b.String()
	}
	return ""
}

// ---- UpdateXxx 执行函数 ----

func (tc *tableCode) updateFuncSection() string {
	spec := tc.spec
	filterDoc := ""
	if len(spec.Filters) > 0 {
		filterDoc = "// @filter 不变量恒定 AND 进 WHERE;WithoutFilter() 可整体绕过,但不豁免\n// 零 where 守卫。\n"
	}
	noPkDoc := ""
	if !spec.ReadEnabled {
		noPkDoc = "//\n// 本表无 @pk:零 where 守卫只保证\"至少一个条件\",条件可能命中多行;\n// 调用方必须自带唯一组合条件(如全部业务键列),否则会批量改写。\n"
	}
	var p printer
	p.Raw(fill(updateFuncTail,
		"res", tc.res,
		"sing", spec.Singular,
		"dbtx", tc.dbtx,
		"filterDoc", filterDoc,
		"noPkDoc", noPkDoc,
		"filterConds", tc.filterCondsBlock(),
		"cap", condsCap(len(spec.Filters)),
		"table", strconv.Quote(tableNameQualified(spec.Name, tc.opts)),
	))
	return p.String()
}

// filterCondsBlock 生成 @filter 不变量的条件叠加语句块;无声明时为空。
func (tc *tableCode) filterCondsBlock() string {
	if len(tc.spec.Filters) == 0 {
		return ""
	}
	var p printer
	p.Raw(filterCondsHead)
	for i := range tc.spec.Filters {
		p.Linef("\t\tconds = append(conds, %s)", tc.filterExpr(&tc.spec.Filters[i]))
	}
	p.Raw(filterCondsTail)
	return p.String()
}

// condsCap 返回 WHERE 条件切片的预分配容量表达式。
func condsCap(filters int) string {
	if filters == 0 {
		return "len(st.Wheres())"
	}
	return fmt.Sprintf("%d+len(st.Wheres())", filters)
}

// ---- UpdateXxxReturning / UpdateXxxReturningByPk 执行函数 ----

// updateReturningSection 生成 RETURNING 入口:多行形态产出给所有更新面表;
// 主键单行形态只产出给有 @pk 的表(无 @pk 表的动态 WHERE 不保证唯一,
// 单行语义不成立)。是否产出由生成期按现有类型信息(@pk)自动判断,
// 不提供 opt-in 开关。
func (tc *tableCode) updateReturningSection() string {
	var p printer
	p.Raw(tc.updateReturningFuncSection(false))
	if tc.spec.ReadEnabled {
		p.Raw(tc.updateReturningFuncSection(true))
	}
	return p.String()
}

// returningCols 是 RETURNING 列清单:全部物理列名按 schema 声明序拼接
// (保留字引号化,顺序确定 → SQL 稳定)。模型行是强类型(sqlc-gen-go 同类型
// 结构体),须含全部列,@internal 只是协议可见性概念,不影响整行读回。
func (tc *tableCode) returningCols() string {
	names := make([]string, 0, len(tc.spec.Columns))
	for i := range tc.spec.Columns {
		names = append(names, tc.spec.Columns[i].SQLName)
	}
	return "RETURNING " + strings.Join(names, ", ")
}

// pkColumn 返回 @pk 列(仅 ReadEnabled 表调用;@pk 由 DSL 限定单列)。
func (tc *tableCode) pkColumn() *ColumnSpec {
	for i := range tc.spec.Columns {
		if tc.spec.Columns[i].Key == tc.spec.Tiebreaker {
			return &tc.spec.Columns[i]
		}
	}
	return nil
}

// updateReturningFuncSection 生成单个 RETURNING 执行函数。
// 与 UpdateXxx 同源:同一套选项收集、SET 白名单、@filter 恒定不变量与
// dynq.UpdateState 校验,RETURNING 只扩大读回,不改变写面语义。
// 多行形态用 CollectRows(零命中 = 空切片 + nil error);单行形态用
// CollectOneRow(零命中 = pgx.ErrNoRows),主键定位经 dynq.Locate 内建,
// 与调用方 Where 条件 AND 且不占槽位。
func (tc *tableCode) updateReturningFuncSection(byPk bool) string {
	spec := tc.spec
	model := tc.modelPkg + spec.ModelName
	var p printer
	zero, collect := "nil", "pgx.CollectRows"
	if byPk {
		zero, collect = model+"{}", "pgx.CollectOneRow"
		pk := tc.pkColumn()
		p.Raw(fill(returningByPkHead,
			"res", tc.res,
			"sing", spec.Singular,
			"model", model,
			"dbtx", tc.dbtx,
			"pkKey", pk.Key,
			"pkType", pk.GoType,
			"pkConst", pk.GoConst,
			"locateSQL", strconv.Quote(pk.SQLName+" = ?"+pk.whereCast()),
		))
	} else {
		noPkDoc := ""
		if !spec.ReadEnabled {
			noPkDoc = "//\n// 本表无 @pk:零 where 守卫只保证\"至少一个条件\",条件可能命中多行——\n// 被改写行会全部返回;调用方必须自带唯一组合条件(如全部业务键列),\n// 否则会批量改写。\n"
		}
		p.Raw(fill(returningMultiHead,
			"res", tc.res,
			"sing", spec.Singular,
			"model", model,
			"dbtx", tc.dbtx,
			"noPkDoc", noPkDoc,
		))
	}
	p.Raw(fill(returningTail,
		"filterConds", tc.filterCondsBlock(),
		"cap", condsCap(len(spec.Filters)),
		"table", strconv.Quote(tableNameQualified(spec.Name, tc.opts)),
		"returning", strconv.Quote(tc.returningCols()),
		"zero", zero,
		"collect", collect,
		"model", model,
	))
	return p.String()
}
