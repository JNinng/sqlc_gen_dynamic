package main

import (
	"fmt"
	"strings"
)

// gen_query.go:Parse / validate / where / cursor 取值 / List / Count / Schema 段。

// Cast 返回 seek 谓词与 where 等值比较需要的显式 cast 后缀。
// 文本形态参数(string)与 uuid/enum 列比较、[]byte/[]int64 与 jsonb/int_array
// 列比较均需显式 cast,不依赖预编译语句的参数类型推断。
func (c *ColumnSpec) Cast() string {
	switch c.DataType {
	case "uuid":
		return "::uuid"
	case "enum":
		return "::" + c.EnumPhysical
	default:
		return ""
	}
}

// whereCast 是 where 构造的 cast 后缀(比 Cast 多覆盖 jsonb / int_array / numeric)。
func (c *ColumnSpec) whereCast() string {
	switch c.DataType {
	case "uuid":
		return "::uuid"
	case "enum":
		return "::" + c.EnumPhysical
	case "jsonb":
		return "::jsonb"
	case "int_array":
		return "::int4[]"
	default:
		return ""
	}
}

// ---- ParseUsersQuery(协议门) ----

func (tc *tableCode) parseSection() string {
	spec := tc.spec
	var b strings.Builder
	fmt.Fprintf(&b, "// Parse%sQuery 是协议门:将严格解码后的 JSON 查询信封转换为 typed 查询参数。\n", tc.res)
	b.WriteString("// 解析过程为穷举 switch,零反射;排序回落与游标内容校验统一在查询函数执行。\n")
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - q:   查询信封,需先经 dynq.DecodeQuery 严格解码。\n")
	b.WriteString("//   - opts: 运行期能力收窄选项;禁用目标不在 registry 时返回错误。\n")
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	fmt.Fprintf(&b, "//   - List%sParams: 解析后的查询参数,可直接传给 List%s/Count%s。\n", tc.res, tc.res, tc.res)
	b.WriteString("//   - error:          解析失败时的结构化错误(字段未知、op 越权、条件冲突、类型不符等)。\n")
	fmt.Fprintf(&b, "func Parse%sQuery(q *dynq.Query, opts ...dynq.QueryOption) (List%sParams, error) {\n", tc.res, tc.res)
	b.WriteString("\tcfg, err := dynq.NewQueryConfig(" + tc.reg + "Registry, opts...)\n")
	b.WriteString("\tif err != nil {\n\t\treturn List" + tc.res + "Params{}, err\n\t}\n")
	b.WriteString("\tvar out List" + tc.res + "Params\n")
	// slots 仅被携带 op 的列引用(每个 op case 都执行槽位占用);零 ops 表
	// 不生成,避免悬空变量。
	hasOps := false
	for _, c := range spec.Columns {
		if len(c.Ops) > 0 {
			hasOps = true
			break
		}
	}
	if hasOps {
		b.WriteString("\tslots := dynq.NewSlots()\n")
	}
	b.WriteString("\tbudget := dynq.NewBudget()\n\n")
	// fields
	b.WriteString("\t// 解析投影字段:显式空数组、@hidden 字段、被禁用字段与未知字段均被拒绝;重复字段去重并保持顺序。\n")
	fmt.Fprintf(&b, "\tselectFields, err := dynq.ParseSelectFields(q.Fields, cfg, %sRegistry)\n", tc.reg)
	b.WriteString("\tif err != nil {\n\t\treturn out, err\n\t}\n")
	b.WriteString("\tout.SelectFields = selectFields\n\n")
	// where
	b.WriteString("\t// 解析过滤条件:条件之间恒为 AND 语义;条件条数与占位符预算均受上限约束。\n")
	b.WriteString("\tif len(q.Where) > dynq.MaxWhere {\n\t\treturn out, dynq.ErrTooManyConditions()\n\t}\n")
	b.WriteString("\tfor i, c := range q.Where {\n")
	fmt.Fprintf(&b, "\t\tidx, ok := %sFieldIndex[dynq.FieldName(c.Field)]\n", tc.reg)
	b.WriteString("\t\tif !ok {\n\t\t\treturn out, dynq.NewErr(dynq.KindWhere, c.Field, \"unknown field\", i)\n\t\t}\n")
	fmt.Fprintf(&b, "\t\tf := %sRegistry.Fields[idx]\n", tc.reg)
	b.WriteString("\t\tif f.Hidden {\n\t\t\treturn out, dynq.NewErr(dynq.KindWhere, c.Field, \"field hidden\", i)\n\t\t}\n")
	b.WriteString("\t\tif cfg.WhereDisabled(f.Key) {\n\t\t\treturn out, dynq.NewErr(dynq.KindWhere, c.Field, \"field disabled in this context\", i)\n\t\t}\n")
	b.WriteString("\t\top, err := dynq.ParseOp(c.Op)\n")
	b.WriteString("\t\tif err != nil {\n\t\t\treturn out, dynq.NewErr(dynq.KindWhere, c.Field, \"unknown op: \"+c.Op, i)\n\t\t}\n")
	b.WriteString("\t\tif !f.HasOp(op) {\n\t\t\treturn out, dynq.NewErr(dynq.KindWhere, c.Field, \"op not allowed: \"+c.Op, i)\n\t\t}\n")
	b.WriteString("\t\tif err := budget.Add(1); err != nil {\n\t\t\treturn out, dynq.AsError(err).At(dynq.KindWhere, i, c.Field)\n\t\t}\n")
	b.WriteString("\t\tif op == dynq.OpIsNull || op == dynq.OpNotNull {\n")
	b.WriteString("\t\t\tif c.Value != nil || c.RightValue != nil {\n\t\t\t\treturn out, dynq.NewErr(dynq.KindWhere, c.Field, \"is_null/not_null must not carry value or right_value\", i)\n\t\t\t}\n\t\t}\n")
	b.WriteString("\t\tswitch f.Key {\n")
	for _, c := range spec.Columns {
		if len(c.Ops) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\t\tcase %q:\n", c.Key)
		b.WriteString("\t\t\tswitch op {\n")
		for _, op := range orderedOps(c.Ops) {
			b.WriteString(tc.parseOpCase(&c, op))
		}
		b.WriteString("\t\t\t}\n")
	}
	b.WriteString("\t\t}\n")
	b.WriteString("\t}\n")
	// is_null / not_null 互斥(跨条件)
	for _, c := range spec.Columns {
		if c.HasOp("is_null") && c.HasOp("not_null") {
			fmt.Fprintf(&b, "\tif out.%sIsNull && out.%sNotNull {\n", c.GoConst, c.GoConst)
			fmt.Fprintf(&b, "\t\treturn out, dynq.NewErr(dynq.KindWhere, %q, \"is_null and not_null are mutually exclusive\", -1)\n\t}\n", c.Key)
		}
	}
	// sort
	b.WriteString("\n\t// 解析排序:字段须在注册表内且可排序;方向归一化为 asc/desc,并转为逻辑键。\n")
	b.WriteString("\tfor i, s := range q.Sort {\n")
	fmt.Fprintf(&b, "\t\tidx, ok := %sFieldIndex[dynq.FieldName(s.Field)]\n", tc.reg)
	b.WriteString("\t\tif !ok {\n\t\t\treturn out, dynq.NewErr(dynq.KindSort, s.Field, \"unknown field\", i)\n\t\t}\n")
	fmt.Fprintf(&b, "\t\tf := %sRegistry.Fields[idx]\n", tc.reg)
	b.WriteString("\t\tif f.Hidden {\n\t\t\treturn out, dynq.NewErr(dynq.KindSort, s.Field, \"field hidden\", i)\n\t\t}\n")
	b.WriteString("\t\tif !f.Sortable {\n\t\t\treturn out, dynq.NewErr(dynq.KindSort, s.Field, \"field not sortable\", i)\n\t\t}\n")
	b.WriteString("\t\tif cfg.SortDisabled(f.Key) {\n\t\t\treturn out, dynq.NewErr(dynq.KindSort, s.Field, \"field disabled in this context\", i)\n\t\t}\n")
	b.WriteString("\t\tdesc, err := dynq.NormalizeOrder(s.Order)\n")
	b.WriteString("\t\tif err != nil {\n\t\t\treturn out, dynq.NewErr(dynq.KindSort, s.Field, \"invalid order: \"+s.Order, i)\n\t\t}\n")
	b.WriteString("\t\tout.OrderBy = append(out.OrderBy, dynq.OrderBy{Field: f.Key, Desc: desc})\n")
	b.WriteString("\t}\n")
	// pagination
	b.WriteString("\n\t// 解析分页:判定 offset/游标模式并归一化(page→Offset、pageSize→Limit)。\n")
	fmt.Fprintf(&b, "\tlimit, offset, after, err := dynq.NormalizePagination(%sRegistry, q.Pagination)\n", tc.reg)
	b.WriteString("\tif err != nil {\n\t\treturn out, err\n\t}\n")
	b.WriteString("\tout.Limit, out.Offset, out.After = limit, offset, after\n")
	b.WriteString("\treturn out, nil\n")
	b.WriteString("}\n")
	return b.String()
}

// ---- validateUsersParams(typed 门共享形态复核) ----

// paramRefExprs 返回某字段全部 where Params 槽位的"非零值"表达式(按字段名去重)。
func paramRefExprs(c *ColumnSpec) []string {
	var exprs []string
	seen := map[string]bool{}
	add := func(name, expr string) {
		if seen[name] {
			return
		}
		seen[name] = true
		exprs = append(exprs, expr)
	}
	for _, op := range orderedOps(c.Ops) {
		switch op {
		case "between":
			add(fmt.Sprintf("%sGte", c.GoConst), fmt.Sprintf("params.%sGte != nil", c.GoConst))
			add(fmt.Sprintf("%sLte", c.GoConst), fmt.Sprintf("params.%sLte != nil", c.GoConst))
		case "eq":
			add(c.GoConst, fmt.Sprintf("params.%s != nil", c.GoConst))
		case "neq", "gt", "gte", "lt", "lte":
			add(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), fmt.Sprintf("params.%s%s != nil", c.GoConst, opSuffix[op]))
		case "in", "not_in":
			add(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), fmt.Sprintf("params.%s%s != nil", c.GoConst, opSuffix[op]))
		case "contains", "prefix", "suffix":
			add(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), fmt.Sprintf("params.%s%s != \"\"", c.GoConst, opSuffix[op]))
		case "is_null", "not_null":
			add(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), fmt.Sprintf("params.%s%s", c.GoConst, opSuffix[op]))
		case "has":
			add(fmt.Sprintf("%sHas", c.GoConst), fmt.Sprintf("params.%sHas != nil", c.GoConst))
		}
	}
	return exprs
}

func (c *ColumnSpec) HasOp(op string) bool {
	for _, o := range c.Ops {
		if o == op {
			return true
		}
	}
	return false
}

func (tc *tableCode) validateSection() string {
	spec := tc.spec
	var b strings.Builder
	fmt.Fprintf(&b, "// validate%sParams 校验 params 在当前 opts 下的合法性,List 与 Count 共用。\n", tc.res)
	b.WriteString("// 禁用字段携带非零值、IsNull 与 NotNull 同时为真均显式报错,不做静默剔除")
	if len(spec.Scope) > 0 {
		b.WriteString(";\n// 声明 @scope 的资源缺省 Scope 时 fail-closed 报错。\n")
	} else {
		b.WriteString("。\n")
	}
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - params: 待复核的查询参数。\n")
	b.WriteString("//   - cfg:    经 opts 投影后的运行期配置。\n")
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	b.WriteString("//   - error: 复核通过返回 nil,否则返回结构化错误。\n")
	fmt.Fprintf(&b, "func validate%sParams(params List%sParams, cfg *dynq.QueryConfig) error {\n", tc.res, tc.res)
	if len(spec.Scope) > 0 {
		b.WriteString("\t// 数据权限 fail-closed:声明 @scope 的表缺省 Scope 时显式报错,\n")
		b.WriteString("\t// 宁可拒绝服务也不静默退化为全量可见。\n")
		b.WriteString("\tif params.Scope == nil {\n\t\treturn dynq.ErrScopeRequired()\n\t}\n")
	}
	for _, c := range spec.Columns {
		exprs := paramRefExprs(&c)
		if len(exprs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\tif cfg.WhereDisabled(%q) && (%s) {\n", c.Key, strings.Join(exprs, " || "))
		fmt.Fprintf(&b, "\t\treturn dynq.NewErr(dynq.KindWhere, %q, \"field disabled in this context\", -1)\n\t}\n", c.Key)
	}
	for _, c := range spec.Columns {
		if c.HasOp("is_null") && c.HasOp("not_null") {
			fmt.Fprintf(&b, "\tif params.%sIsNull && params.%sNotNull {\n", c.GoConst, c.GoConst)
			fmt.Fprintf(&b, "\t\treturn dynq.NewErr(dynq.KindWhere, %q, \"is_null and not_null are mutually exclusive\", -1)\n\t}\n", c.Key)
		}
	}
	fmt.Fprintf(&b, "\treturn dynq.ValidateTypedSelectFields(params.SelectFields, cfg, %sRegistry)\n", tc.reg)
	b.WriteString("}\n")
	return b.String()
}

// ---- buildUsersWhere(List/Count 唯一过滤事实源) ----

func (tc *tableCode) whereSection() string {
	spec := tc.spec
	var b strings.Builder
	fmt.Fprintf(&b, "// build%sWhere 构建过滤 WHERE 条件,List 与 Count 共用,过滤结果一致。\n", tc.res)
	b.WriteString("// 所有值均经占位符参数化,列名与操作符为白名单字面量")
	if len(spec.Scope) > 0 {
		b.WriteString(";\n// 声明 @scope 的表头部注入数据权限谓词(守卫函数名同为生成期白名单字面量),\n// 与业务条件 AND 组合,total 与 items 的权限视图天然一致\n")
	} else {
		b.WriteString("。\n")
	}
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - params: 已通过 validate 形态复核的查询参数。\n")
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	b.WriteString("//   - []sq.Sqlizer: 按 AND 语义组合的过滤条件列表。\n")
	fmt.Fprintf(&b, "func build%sWhere(params List%sParams) []sq.Sqlizer {\n", tc.res, tc.res)
	b.WriteString("\tvar conds []sq.Sqlizer\n")
	if len(spec.Scope) > 0 {
		b.WriteString("\t// 数据权限谓词(@scope):多守卫 OR 组合,与业务条件 AND;列名/操作符/\n")
		b.WriteString("\t// 守卫函数名/字面量参数为生成期白名单字面量,标识符参数经占位符参数化;\n")
		b.WriteString("\t// IN-子查询与不相关标量函数在单条语句内只求值一次;params.Scope 非 nil\n")
		b.WriteString("\t// 已由 validate 保证。\n")
		exprs := make([]string, 0, len(spec.Scope))
		for i := range spec.Scope {
			exprs = append(exprs, tc.scopeGuardExpr(&spec.Scope[i]))
		}
		if len(exprs) == 1 {
			fmt.Fprintf(&b, "\tconds = append(conds, %s)\n", exprs[0])
		} else {
			b.WriteString("\tconds = append(conds, sq.Or{\n")
			for _, e := range exprs {
				fmt.Fprintf(&b, "\t\t%s,\n", e)
			}
			b.WriteString("\t})\n")
		}
	}
	for _, c := range spec.Columns {
		for _, op := range orderedOps(c.Ops) {
			for _, line := range tc.whereCaseLines(&c, op) {
				b.WriteString("\t" + line + "\n")
			}
		}
	}
	b.WriteString("\treturn conds\n}\n")
	return b.String()
}

// whereCaseLines 生成单个 (字段, op) 的 if-append 语句(单行)。
func (tc *tableCode) whereCaseLines(c *ColumnSpec, op string) []string {
	col := c.Physical
	cast := c.whereCast()
	cond := func(condExpr string, builder string) string {
		return fmt.Sprintf("if %s { conds = append(conds, %s) }", condExpr, builder)
	}
	switch op {
	case "eq":
		if cast != "" {
			return []string{cond(fmt.Sprintf("params.%s != nil", c.GoConst),
				fmt.Sprintf("sq.Expr(%q, *params.%s)", col+" = ?"+cast, c.GoConst))}
		}
		return []string{cond(fmt.Sprintf("params.%s != nil", c.GoConst),
			fmt.Sprintf("sq.Eq{%q: *params.%s}", col, c.GoConst))}
	case "neq":
		if cast != "" {
			return []string{cond(fmt.Sprintf("params.%sNeq != nil", c.GoConst),
				fmt.Sprintf("sq.Expr(%q, *params.%sNeq)", col+" <> ?"+cast, c.GoConst))}
		}
		return []string{cond(fmt.Sprintf("params.%sNeq != nil", c.GoConst),
			fmt.Sprintf("sq.NotEq{%q: *params.%sNeq}", col, c.GoConst))}
	case "in", "not_in":
		field := fmt.Sprintf("params.%s%s", c.GoConst, opSuffix[op])
		if c.DataType == "int_array" {
			// in(int_array 列)= && 数组重叠语义。
			return []string{cond(field+" != nil",
				fmt.Sprintf("sq.Expr(%q, %s)", col+" && ?"+cast, field))}
		}
		if cast != "" {
			inOp := "IN"
			if op == "not_in" {
				inOp = "NOT IN"
			}
			expr := fmt.Sprintf("sq.Expr(\"%s %s (\"+dynq.PlaceholdersCast(len(%s), %q)+\")\", dynq.Anys(%s)...)",
				col, inOp, field, cast, field)
			return []string{cond(field+" != nil", expr)}
		}
		if op == "not_in" {
			return []string{cond(field+" != nil",
				fmt.Sprintf("sq.NotEq{%q: %s}", col, field))}
		}
		return []string{cond(field+" != nil",
			fmt.Sprintf("sq.Eq{%q: %s}", col, field))}
	case "gt", "gte", "lt", "lte":
		field := fmt.Sprintf("params.%s%s", c.GoConst, opSuffix[op])
		sqlop := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[op]
		if cast != "" {
			return []string{cond(field+" != nil",
				fmt.Sprintf("sq.Expr(%q, *%s)", col+" "+sqlop+" ?"+cast, field))}
		}
		sqmap := map[string]string{"gt": "sq.Gt", "gte": "sq.GtOrEq", "lt": "sq.Lt", "lte": "sq.LtOrEq"}[op]
		return []string{cond(field+" != nil",
			fmt.Sprintf("%s{%q: *%s}", sqmap, col, field))}
	case "contains", "prefix", "suffix":
		pattern := map[string]string{
			"contains": "dynq.ContainsPattern", "prefix": "dynq.PrefixPattern", "suffix": "dynq.SuffixPattern",
		}[op]
		return []string{cond(fmt.Sprintf("params.%s%s != \"\"", c.GoConst, opSuffix[op]),
			fmt.Sprintf("sq.Expr(%q, %s(params.%s%s))",
				col+" ILIKE ? ESCAPE '\\'", pattern, c.GoConst, opSuffix[op]))}
	case "is_null":
		return []string{cond(fmt.Sprintf("params.%sIsNull", c.GoConst), fmt.Sprintf("sq.Expr(%q)", col+" IS NULL"))}
	case "not_null":
		return []string{cond(fmt.Sprintf("params.%sNotNull", c.GoConst), fmt.Sprintf("sq.Expr(%q)", col+" IS NOT NULL"))}
	case "has":
		return []string{cond(fmt.Sprintf("params.%sHas != nil", c.GoConst),
			fmt.Sprintf("sq.Expr(%q, string(params.%sHas))", col+" @> ?"+cast, c.GoConst))}
	}
	return nil
}

// scopeOpSQL 是标量守卫操作符的 SQL 形态(in 走集合子查询特殊形态)。
var scopeOpSQL = map[string]string{"eq": "=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}

// scopeGuardExpr 生成单个守卫的 sq.Expr(...) 语句:标识符参数以占位符引用
// params.Scope 字段(jsonb 参数显式 cast,不依赖参数类型推断),字面量参数
// 经分词器校验后内联;in 为集合守卫(IN (SELECT * FROM fn(...))),
// in_any 为数组三态守卫((fn(...) IS NULL OR col = ANY(fn(...))):NULL=放行、
// {}=拒绝、非空=ANY 匹配),其余为标量比较(col = fn(...) 等,NULL 列对
// 比较恒为假,不可见,fail-closed 方向)。
func (tc *tableCode) scopeGuardExpr(g *scopeGuard) string {
	var callArgs, valExprs []string
	for _, a := range g.Args {
		if a.Literal != "" {
			if a.Quoted {
				callArgs = append(callArgs, "'"+a.Literal+"'")
			} else {
				callArgs = append(callArgs, a.Literal)
			}
			continue
		}
		var p *scopeParam
		for i := range tc.spec.ScopeParams {
			if tc.spec.ScopeParams[i].Name == a.Name {
				p = &tc.spec.ScopeParams[i]
				break
			}
		}
		placeholder := "?"
		if p.GoType == "json.RawMessage" {
			placeholder = "?::jsonb"
		}
		callArgs = append(callArgs, placeholder)
		valExprs = append(valExprs, "params.Scope."+p.GoConst)
	}
	call := g.Fn + "(" + strings.Join(callArgs, ", ") + ")"
	frag := g.Column + " " + scopeOpSQL[g.Op] + " " + call
	if g.Op == "in" {
		frag = g.Column + " IN (SELECT * FROM " + call + ")"
	}
	if g.Op == "in_any" {
		// frag 自带外层括号:sq.Expr 不参与 squirrel 的自动加括号,顶层 OR
		// 若裸露会与业务条件的 AND 发生优先级错乱(NULL 放行将绕过业务过滤)。
		// fn 调用出现两次 → 占位参数相应双份。
		frag = "(" + call + " IS NULL OR " + g.Column + " = ANY(" + call + "))"
		valExprs = append(valExprs, valExprs...)
	}
	if len(valExprs) == 0 {
		return fmt.Sprintf("sq.Expr(%q)", frag)
	}
	return fmt.Sprintf("sq.Expr(%q, %s)", frag, strings.Join(valExprs, ", "))
}
