package main

import (
	"fmt"
	"strings"
)

// gen_exec.go:游标取值 / List / Count / Schema 段。

// ---- usersCursorValues(模型行 → 有效排序列值) ----

// cursorValueExpr 返回模型行字段取值表达式(按 dataType 归一化为可编码值)。
func cursorValueExpr(c *ColumnSpec) string {
	ref := "row." + c.ModelField
	switch c.DataType {
	case "enum":
		// 枚举类型(字符串底层)显式转 string,统一游标编码路径。
		return "string(" + ref + ")"
	default:
		return ref
	}
}

func (tc *tableCode) cursorValuesSection() string {
	spec := tc.spec
	var b strings.Builder
	fmt.Fprintf(&b, "// %sCursorValues 提取用于构造 nextCursor 的排序列值,顺序与有效排序一致。\n", lowerFirst(spec.Name))
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - row: 查询结果中的一行模型。\n")
	b.WriteString("//   - seq: 排序列及方向,顺序与游标值顺序一致。\n")
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	b.WriteString("//   - []any: 与 seq 一一对应的列值切片。\n")
	fmt.Fprintf(&b, "func %sCursorValues(row %s, seq []dynq.SortField) []any {\n", lowerFirst(spec.Name), tc.modelPkg+spec.ModelName)
	b.WriteString("\tvals := make([]any, len(seq))\n")
	b.WriteString("\tfor i, sf := range seq {\n")
	b.WriteString("\t\tswitch sf.Key {\n")
	for _, c := range spec.Columns {
		if !c.Sortable && c.Key != spec.Tiebreaker {
			continue
		}
		fmt.Fprintf(&b, "\t\tcase %q:\n\t\t\tvals[i] = %s\n", c.Key, cursorValueExpr(&c))
	}
	b.WriteString("\t\tdefault:\n\t\t\tvals[i] = nil\n\t\t}\n")
	b.WriteString("\t}\n\treturn vals\n}\n")
	return b.String()
}

// ---- ListUsers ----

func (tc *tableCode) listSection() string {
	spec := tc.spec
	model := tc.modelPkg + spec.ModelName
	table := tableNameQualified(spec.Name, tc.opts)
	var b strings.Builder
	fmt.Fprintf(&b, "// List%s 按 params 的过滤、排序、投影与分页条件查询列表页。\n", tc.res)
	fmt.Fprintf(&b, "// 返回本页行、是否还有下一页与续翻游标;total 由 Count%s 单独计算\n", tc.res)
	b.WriteString("// (非强一致快照)。生成代码为纯函数,不持有连接状态。\n")
	b.WriteString("// 未查询列在结果中留零值(动态投影固有语义),需区分时按请求 fields 做 DTO 化。\n")
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - ctx:    查询上下文。\n")
	b.WriteString("//   - db:     数据库连接,满足 DBTX 接口即可。\n")
	fmt.Fprintf(&b, "//   - params: typed 查询参数,可由 Parse%sQuery 产出或直接构造。\n", tc.res)
	b.WriteString("//   - opts:   运行期能力收窄选项;禁用目标不在 registry 时返回错误。\n")
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	fmt.Fprintf(&b, "//   - dynq.Result[%s]: 本页行、是否还有下一页与续翻游标。\n", model)
	b.WriteString("//   - error:              查询或校验失败时的错误。\n")
	fmt.Fprintf(&b, "func List%s(ctx context.Context, db %s, params List%sParams, opts ...dynq.QueryOption) (dynq.Result[%s], error) {\n", tc.res, tc.dbtx, tc.res, model)
	fmt.Fprintf(&b, "\tcfg, err := dynq.NewQueryConfig(%sRegistry, opts...)\n", tc.reg)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	fmt.Fprintf(&b, "\tif err := validate%sParams(params, cfg); err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", tc.res, model)
	fmt.Fprintf(&b, "\tlimit, err := dynq.CheckPagination(%sRegistry, params.Limit, params.Offset, params.After)\n", tc.reg)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	b.WriteString("\t// 解析有效排序:未指定排序时回落表级 @default_sort。\n")
	fmt.Fprintf(&b, "\tseq, err := dynq.ResolveSort(%sRegistry, cfg, params.OrderBy)\n", tc.reg)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	fmt.Fprintf(&b, "\tfp := dynq.CursorFingerprint(%sRegistry.Resource, seq)\n", tc.reg)
	b.WriteString("\t// 计算 SELECT 列:投影列并集排序列(排序所需列始终参与查询)。\n")
	fmt.Fprintf(&b, "\tcols := dynq.SelectColumns(%sRegistry, params.SelectFields, seq)\n", tc.reg)
	fmt.Fprintf(&b, "\tb := sq.Select(cols...).From(%q).PlaceholderFormat(sq.Dollar).Where(sq.And(build%sWhere(params))).\n", table, tc.res)
	b.WriteString("\t\tOrderBy(dynq.OrderBySQL(seq)).Limit(uint64(limit) + 1)\n")
	b.WriteString("\t// After 非 nil 时为游标模式:解码并校验游标,从该位置继续读取。\n")
	b.WriteString("\tif params.After != nil {\n")
	fmt.Fprintf(&b, "\t\tvals, err := dynq.DecodeCursor(*params.After, fp, seq)\n")
	fmt.Fprintf(&b, "\t\tif err != nil {\n\t\t\treturn dynq.Result[%s]{}, err\n\t\t}\n", model)
	b.WriteString("\t\tseek, err := dynq.SeekPredicate(seq, vals)\n")
	fmt.Fprintf(&b, "\t\tif err != nil {\n\t\t\treturn dynq.Result[%s]{}, err\n\t\t}\n", model)
	b.WriteString("\t\tb = b.Where(seek)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tif params.Offset > 0 {\n\t\tb = b.Offset(uint64(params.Offset))\n\t}\n")
	b.WriteString("\tquery, args, err := b.ToSql()\n")
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	b.WriteString("\trows, err := db.Query(ctx, query, args...)\n")
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	fmt.Fprintf(&b, "\titems, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[%s])\n", model)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	b.WriteString("\t// 以 LIMIT n+1 探测是否还有下一页,并构造 nextCursor。\n")
	b.WriteString("\thasMore := len(items) > int(limit)\n")
	b.WriteString("\tif hasMore {\n\t\titems = items[:limit]\n\t}\n")
	b.WriteString("\tvar next string\n")
	b.WriteString("\tif hasMore {\n")
	fmt.Fprintf(&b, "\t\tnext, err = dynq.EncodeCursor(fp, seq, %sCursorValues(items[len(items)-1], seq))\n", lowerFirst(spec.Name))
	fmt.Fprintf(&b, "\t\tif err != nil {\n\t\t\treturn dynq.Result[%s]{}, err\n\t\t}\n\t}\n", model)
	fmt.Fprintf(&b, "\treturn dynq.Result[%s]{Items: items, HasMore: hasMore, NextCursor: next}, nil\n", model)
	b.WriteString("}\n")
	return b.String()
}

// ---- CountUsers ----

func (tc *tableCode) countSection() string {
	spec := tc.spec
	table := tableNameQualified(spec.Name, tc.opts)
	var b strings.Builder
	fmt.Fprintf(&b, "// Count%s 统计满足过滤条件的行数,与分页模式和游标位置无关;\n", tc.res)
	b.WriteString("// 忽略投影、排序与游标。\n")
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - ctx:    查询上下文。\n")
	b.WriteString("//   - db:     数据库连接,满足 DBTX 接口即可。\n")
	fmt.Fprintf(&b, "//   - params: typed 查询参数,与 List%s 使用同一份形态复核。\n", tc.res)
	b.WriteString("//   - opts:   运行期能力收窄选项。\n")
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	b.WriteString("//   - int64: 满足过滤条件的行数。\n")
	b.WriteString("//   - error: 查询或校验失败时返回错误。\n")
	fmt.Fprintf(&b, "func Count%s(ctx context.Context, db %s, params List%sParams, opts ...dynq.QueryOption) (int64, error) {\n", tc.res, tc.dbtx, tc.res)
	fmt.Fprintf(&b, "\tcfg, err := dynq.NewQueryConfig(%sRegistry, opts...)\n", tc.reg)
	b.WriteString("\tif err != nil {\n\t\treturn 0, err\n\t}\n")
	fmt.Fprintf(&b, "\tif err := validate%sParams(params, cfg); err != nil {\n\t\treturn 0, err\n\t}\n", tc.res)
	fmt.Fprintf(&b, "\tif _, err := dynq.CheckPagination(%sRegistry, params.Limit, params.Offset, params.After); err != nil {\n\t\treturn 0, err\n\t}\n", tc.reg)
	fmt.Fprintf(&b, "\tquery, args, err := sq.Select(\"COUNT(*)\").From(%q).PlaceholderFormat(sq.Dollar).Where(sq.And(build%sWhere(params))).ToSql()\n", table, tc.res)
	b.WriteString("\tif err != nil {\n\t\treturn 0, err\n\t}\n")
	b.WriteString("\tvar total int64\n")
	b.WriteString("\tif err := db.QueryRow(ctx, query, args...).Scan(&total); err != nil {\n\t\treturn 0, err\n\t}\n")
	b.WriteString("\treturn total, nil\n}\n")
	return b.String()
}

// ---- UsersQuerySchema ----

func (tc *tableCode) schemaSection() string {
	var b strings.Builder
	fmt.Fprintf(&b, "// %sQuerySchema 是前端契约出口:返回经 opts 减法投影后的有效能力全集,\n", tc.res)
	b.WriteString("// 前端据此渲染查询组件。Schema 是(生成期元数据, opts)的确定性纯函数,\n")
	b.WriteString("// 同一 opts 产出字节级相同结果,内容 hash 可作 ETag;被禁用字段不出现于 schema。\n")
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - opts: 运行期能力收窄选项,须与 Parse/List/Count 使用同一份 opts。\n")
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	b.WriteString("//   - dynq.ResourceSchema: 前端渲染所需的能力描述(字段、操作符、排序、分页配置)。\n")
	fmt.Fprintf(&b, "func %sQuerySchema(opts ...dynq.QueryOption) dynq.ResourceSchema {\n", tc.res)
	fmt.Fprintf(&b, "\tcfg, err := dynq.NewQueryConfig(%sRegistry, opts...)\n", tc.reg)
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\t// 禁用目标不在 registry 属于编程错误。Parse/List/Count/Schema 四点同参纪律\n")
	b.WriteString("\t\t// 下,此处 panic 显式暴露;静默吞错会掩盖权限收窄失效。\n")
	b.WriteString("\t\tpanic(err)\n\t}\n")
	fmt.Fprintf(&b, "\treturn dynq.BuildSchema(%sRegistry, cfg)\n", tc.reg)
	b.WriteString("}\n")
	return b.String()
}
