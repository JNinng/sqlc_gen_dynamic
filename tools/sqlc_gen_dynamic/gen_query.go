package main

import (
	"fmt"
	"strconv"
	"strings"
)

// gen_query.go:Cast / Parse / validate / where 段(cursor 取值 / List / Count /
// Schema 段见 gen_exec.go)。

// Cast 返回 registry 记录的 seek 谓词 cast 后缀(uuid/enum/decimal 列的
// 十进制字符串/文本形态参数需要,不依赖预编译语句的参数类型推断)。
// where 构造的 cast 后缀见 whereCast(多覆盖 jsonb / int_array)。
func (c *ColumnSpec) Cast() string {
	switch c.DataType {
	case "uuid":
		return "::uuid"
	case "enum":
		return "::" + c.EnumPhysical
	case "decimal":
		// numeric 列的游标值以十进制字符串参数化,?::numeric 交服务端精确解析,
		// 不经 float8 中转(高精度值不失真)。
		return "::numeric"
	default:
		return ""
	}
}

// whereCast 是 where 构造的 cast 后缀(比 Cast 多覆盖 jsonb / int_array)。
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
	case "decimal":
		// numeric 列:float8 参数显式收敛为 numeric,避免列被隐式转 float8
		// 比较(索引失效、高精度 numeric 比较失真)。float4/float8 无需 cast。
		return "::numeric"
	default:
		return ""
	}
}

// ---- ParseUsersQuery(协议门) ----

func (tc *tableCode) parseSection() string {
	spec := tc.spec
	var p printer
	p.Raw(fill(parseQueryHead, "res", tc.res, "reg", tc.reg))
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
		p.Line("\tslots := dynq.NewSlots()")
	}
	p.Raw(fill(parseQueryBody, "reg", tc.reg))

	var cases printer
	for _, c := range spec.Columns {
		if len(c.Ops) == 0 {
			continue
		}
		cases.Linef("\t\tcase %q:", c.Key)
		cases.Line("\t\t\tswitch op {")
		for _, op := range orderedOps(c.Ops) {
			cases.Raw(tc.parseOpCase(&c, op))
		}
		cases.Line("\t\t\t}")
	}
	p.Raw(cases.String())

	// is_null / not_null 互斥(跨条件)
	var nulls printer
	for _, c := range spec.Columns {
		if c.HasOp("is_null") && c.HasOp("not_null") {
			nulls.Linef("\tif out.%sIsNull && out.%sNotNull {", c.GoConst, c.GoConst)
			nulls.Linef("\t\treturn out, dynq.NewErr(dynq.KindWhere, %q, \"is_null and not_null are mutually exclusive\", -1)", c.Key)
			nulls.Line("\t}")
		}
	}
	p.Raw(fill(parseQueryTail, "reg", tc.reg, "nullGuards", nulls.String()))
	return p.String()
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
	scopeNote := "。"
	if len(spec.Scope) > 0 {
		scopeNote = ";\n// 声明 @scope 的资源缺省 Scope 时 fail-closed 报错。"
	}
	var p printer
	p.Raw(fill(validateHead, "res", tc.res, "scopeNote", scopeNote))
	if len(spec.Scope) > 0 {
		p.Raw(validateScopeGuard)
	}
	for _, c := range spec.Columns {
		exprs := paramRefExprs(&c)
		if len(exprs) == 0 {
			continue
		}
		p.Linef("\tif cfg.WhereDisabled(%q) && (%s) {", c.Key, strings.Join(exprs, " || "))
		p.Linef("\t\treturn dynq.NewErr(dynq.KindWhere, %q, \"field disabled in this context\", -1)", c.Key)
		p.Line("\t}")
	}
	for _, c := range spec.Columns {
		if c.HasOp("is_null") && c.HasOp("not_null") {
			p.Linef("\tif params.%sIsNull && params.%sNotNull {", c.GoConst, c.GoConst)
			p.Linef("\t\treturn dynq.NewErr(dynq.KindWhere, %q, \"is_null and not_null are mutually exclusive\", -1)", c.Key)
			p.Line("\t}")
		}
	}
	for _, c := range spec.Columns {
		if !c.HasOp("has") {
			continue
		}
		// 空 jsonb 定位值会生成 @> ''::jsonb 的运行期 PG 错误;协议门的 AsJSON
		// 产不出空字节串,只有直接构造 Params 可达,仍显式拒绝。
		p.Linef("\tif params.%sHas != nil && len(params.%sHas) == 0 {", c.GoConst, c.GoConst)
		p.Linef("\t\treturn dynq.ErrEmptyJSON(%q)", c.Key)
		p.Line("\t}")
	}
	p.Raw(fill(validateTail, "reg", tc.reg))
	return p.String()
}

// ---- buildUsersWhere(List/Count 唯一过滤事实源) ----

func (tc *tableCode) whereSection() string {
	spec := tc.spec
	var p printer
	p.Raw(fill(whereHead, "res", tc.res, "notes", whereNotes(spec)))
	if len(spec.Filters) > 0 {
		// 服务端不变量(@filter):恒定过滤条件,先于 scope 谓词与业务条件。
		p.Raw(whereFilterHead)
		for i := range spec.Filters {
			p.Linef("\tconds = append(conds, %s)", tc.filterExpr(&spec.Filters[i]))
		}
	}
	if len(spec.Scope) > 0 {
		p.Raw(whereScopeHead)
		exprs := make([]string, 0, len(spec.Scope))
		for i := range spec.Scope {
			exprs = append(exprs, tc.scopeGuardExpr(&spec.Scope[i]))
		}
		if len(exprs) == 1 {
			p.Linef("\tconds = append(conds, %s)", exprs[0])
		} else {
			p.Line("\tconds = append(conds, sq.Or{")
			for _, e := range exprs {
				p.Linef("\t\t%s,", e)
			}
			p.Line("\t})")
		}
	}
	for _, c := range spec.Columns {
		for _, op := range orderedOps(c.Ops) {
			for _, line := range tc.whereCaseLines(&c, op) {
				p.Line("\t" + line)
			}
		}
	}
	p.Raw(whereTail)
	return p.String()
}

// whereNotes 生成 buildWhere 文档头里随声明形态变化的那句注解。
func whereNotes(spec *TableSpec) string {
	switch {
	case len(spec.Filters) > 0 && len(spec.Scope) > 0:
		return ";\n// 声明 @filter 的表头部附加服务端固定不变量(协议不可见、调用方不可控),\n// List 与 Count 共用,过滤视图天然一致" +
			";\n// 声明 @scope 的表头部注入数据权限谓词(守卫函数名同为生成期白名单字面量),\n// 与业务条件 AND 组合,total 与 items 的权限视图天然一致"
	case len(spec.Filters) > 0:
		return ";\n// 声明 @filter 的表头部附加服务端固定不变量(协议不可见、调用方不可控),\n// List 与 Count 共用,过滤视图天然一致。"
	case len(spec.Scope) > 0:
		return ";\n// 声明 @scope 的表头部注入数据权限谓词(守卫函数名同为生成期白名单字面量),\n// 与业务条件 AND 组合,total 与 items 的权限视图天然一致"
	default:
		return "。"
	}
}

// whereCaseLines 生成单个 (字段, op) 的 if-append 语句(单行)。
func (tc *tableCode) whereCaseLines(c *ColumnSpec, op string) []string {
	col := c.SQLName
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
		// 左列显式 cast 到 jsonb:json(非 jsonb)列没有 json @> jsonb 运算符,
		// 运行期才报错;(col::jsonb) 对 jsonb 列是 no-op,两型共用一条形态。
		return []string{cond(fmt.Sprintf("params.%sHas != nil", c.GoConst),
			fmt.Sprintf("sq.Expr(%q, string(params.%sHas))", "("+col+"::jsonb) @> ?::jsonb", c.GoConst))}
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
	gcol := sqlIdent(g.Column)
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
	frag := gcol + " " + scopeOpSQL[g.Op] + " " + call
	if g.Op == "in" {
		frag = gcol + " IN (SELECT * FROM " + call + ")"
	}
	if g.Op == "in_any" {
		// frag 自带外层括号:sq.Expr 不参与 squirrel 的自动加括号,顶层 OR
		// 若裸露会与业务条件的 AND 发生优先级错乱(NULL 放行将绕过业务过滤)。
		// fn 调用出现两次 → 占位参数相应双份。
		frag = "(" + call + " IS NULL OR " + gcol + " = ANY(" + call + "))"
		valExprs = append(valExprs, valExprs...)
	}
	if len(valExprs) == 0 {
		return fmt.Sprintf("sq.Expr(%q)", frag)
	}
	return fmt.Sprintf("sq.Expr(%q, %s)", frag, strings.Join(valExprs, ", "))
}

// filterExpr 生成单个 @filter 固定条件的 sq.Expr(...) 语句:is_null/not_null
// 为无参谓词;eq/比较类的字面量经分词器校验后作为 Go 字面量经占位符传入
// (不拼进 SQL 文本,零注入面;字符串字面量经 strconv.Quote 安全落源码)。
func (tc *tableCode) filterExpr(f *filterDirective) string {
	col := sqlIdent(f.Column)
	switch f.Op {
	case "is_null":
		return fmt.Sprintf("sq.Expr(%q)", col+" IS NULL")
	case "not_null":
		return fmt.Sprintf("sq.Expr(%q)", col+" IS NOT NULL")
	}
	frag := col + " " + scopeOpSQL[f.Op] + " ?"
	if f.Quoted {
		return fmt.Sprintf("sq.Expr(%q, %s)", frag, strconv.Quote(f.Literal))
	}
	return fmt.Sprintf("sq.Expr(%q, %s)", frag, f.Literal)
}
