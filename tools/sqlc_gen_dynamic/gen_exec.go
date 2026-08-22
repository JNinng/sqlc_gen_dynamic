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
	fmt.Fprintf(&b, "// %sCursorValues 从模型行提取有效排序(含 tiebreaker)依序的列值。\n", lowerFirst(spec.Name))
	b.WriteString("// 投影并集不变量保证这些列必然在 SELECT 中(窄投影下排序列强制并入)。\n")
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
	fmt.Fprintf(&b, "// List%s 执行动态查询:opts → 能力投影 → 形态复核 → 有效排序 → 游标校验\n", tc.res)
	b.WriteString("// → 投影并集 → 过滤 WHERE → seek → LIMIT n+1。生成代码为纯函数,\n")
	fmt.Fprintf(&b, "// 不持有连接状态;total 由 Count%s 单独承担(非强一致快照)。\n", tc.res)
	b.WriteString("// 扫描用 RowToStructByNameLax:未查询列留零值(动态投影固有语义)。\n")
	fmt.Fprintf(&b, "func List%s(ctx context.Context, db %s, params List%sParams, opts ...dynq.QueryOption) (dynq.Result[%s], error) {\n", tc.res, tc.dbtx, tc.res, model)
	fmt.Fprintf(&b, "\tcfg, err := dynq.NewQueryConfig(%sRegistry, opts...)\n", tc.reg)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	fmt.Fprintf(&b, "\tif err := validate%sParams(params, cfg); err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", tc.res, model)
	fmt.Fprintf(&b, "\tlimit, err := dynq.CheckPagination(%sRegistry, params.Limit, params.Offset, params.After)\n", tc.reg)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	b.WriteString("\t// ③ 有效排序解析:用户排序 / @default_sort 回落 / 主键 tiebreaker / 显式 NULLS 方位。\n")
	fmt.Fprintf(&b, "\tseq, err := dynq.ResolveSort(%sRegistry, cfg, params.OrderBy)\n", tc.reg)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn dynq.Result[%s]{}, err\n\t}\n", model)
	fmt.Fprintf(&b, "\tfp := dynq.CursorFingerprint(%sRegistry.Resource, seq)\n", tc.reg)
	b.WriteString("\t// ⑤ 投影并集:(SelectFields 映射列 | 全部非 hidden 列)∪ 有效排序列(含 tiebreaker)。\n")
	fmt.Fprintf(&b, "\tcols := dynq.SelectColumns(%sRegistry, params.SelectFields, seq)\n", tc.reg)
	fmt.Fprintf(&b, "\tb := sq.Select(cols...).From(%q).PlaceholderFormat(sq.Dollar).Where(sq.And(build%sWhere(params))).\n", table, tc.res)
	b.WriteString("\t\tOrderBy(dynq.OrderBySQL(seq)).Limit(uint64(limit) + 1)\n")
	b.WriteString("\t// ④⑦ 游标解码 + 指纹比对 + 值类型收敛 + seek 谓词(双门唯一路径,仅 List 追加)。\n")
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
	b.WriteString("\t// ⑨ LIMIT n+1 探测 has_more;next_cursor 由第 limit 行的有效排序列值构造。\n")
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
	fmt.Fprintf(&b, "// Count%s 只吃过滤 WHERE(total 语义 = 满足过滤条件的总数,与分页模式、\n", tc.res)
	b.WriteString("// 游标位置无关);忽略投影、排序与游标,参数形态错误仍由共享复核拦截。\n")
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
	fmt.Fprintf(&b, "// %sQuerySchema 是前端契约出口:返回经 opts 减法投影后的有效能力全集。\n", tc.res)
	b.WriteString("// Schema 是(生成期元数据, opts)的确定性纯函数:同 opts 产出字节级相同\n")
	b.WriteString("// schema,内容 hash 作 ETag;被禁用字段不出现在 schema 中。\n")
	fmt.Fprintf(&b, "func %sQuerySchema(opts ...dynq.QueryOption) dynq.ResourceSchema {\n", tc.res)
	fmt.Fprintf(&b, "\tcfg, err := dynq.NewQueryConfig(%sRegistry, opts...)\n", tc.reg)
	b.WriteString("\tif err != nil {\n")
	b.WriteString("\t\t// 禁用目标不在 registry 是编程错误:Parse/List/Count/Schema 四点同参\n")
	b.WriteString("\t\t// 纪律下此处 panic 显式暴露,绝不静默吞掉制造安全假象。\n")
	b.WriteString("\t\tpanic(err)\n\t}\n")
	fmt.Fprintf(&b, "\treturn dynq.BuildSchema(%sRegistry, cfg)\n", tc.reg)
	b.WriteString("}\n")
	return b.String()
}
