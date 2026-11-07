package main

import "strconv"

// gen_exec.go:游标取值 / List / Count / Schema 段。
// 静态骨架见 templates.go;本文件只负责把表级/列级元数据填进骨架。

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
	fn := lowerFirst(spec.Name)
	var cases printer
	for _, c := range spec.Columns {
		if !c.Sortable && c.Key != spec.Tiebreaker {
			continue
		}
		cases.Linef("\t\tcase %q:", c.Key)
		cases.Linef("\t\t\tvals[i] = %s", cursorValueExpr(&c))
	}
	var p printer
	p.Raw(fill(cursorValuesSkel,
		"fn", fn,
		"model", tc.modelPkg+spec.ModelName,
		"cases", cases.String(),
	))
	return p.String()
}

// ---- ListUsers ----

// ---- ListUsers ----

func (tc *tableCode) listSection() string {
	spec := tc.spec
	var p printer
	p.Raw(fill(listSkel,
		"res", tc.res,
		"model", tc.modelPkg+spec.ModelName,
		"dbtx", tc.dbtx,
		"reg", tc.reg,
		"table", strconv.Quote(tableNameQualified(spec.Name, tc.opts)),
		"fn", lowerFirst(spec.Name),
	))
	return p.String()
}

// ---- CountUsers ----

// ---- CountUsers ----

func (tc *tableCode) countSection() string {
	spec := tc.spec
	var p printer
	p.Raw(fill(countSkel,
		"res", tc.res,
		"dbtx", tc.dbtx,
		"reg", tc.reg,
		"table", strconv.Quote(tableNameQualified(spec.Name, tc.opts)),
	))
	return p.String()
}

// ---- UsersQuerySchema ----

// ---- UsersQuerySchema ----

func (tc *tableCode) schemaSection() string {
	var p printer
	p.Raw(fill(schemaSkel, "res", tc.res, "reg", tc.reg))
	return p.String()
}
