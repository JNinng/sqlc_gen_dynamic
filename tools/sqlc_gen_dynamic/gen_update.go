package main

import (
	"fmt"
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
	var b strings.Builder
	b.WriteString(tc.updateMarkerSection())
	b.WriteString(tc.updateSetSection())
	b.WriteString(tc.updateWithoutFilterSection())
	b.WriteString(tc.updateWhereSection())
	b.WriteString(tc.updateFuncSection())
	return b.String()
}

// ---- Table marker 与选项别名 ----

func (tc *tableCode) updateMarkerSection() string {
	sing := tc.spec.Singular
	var b strings.Builder
	fmt.Fprintf(&b, "// %sTable 是本表 Update 方法族的编译期 marker:选项类型按表绑定,\n", sing)
	b.WriteString("// 跨表误用(SET / WHERE 选项串表)在编译期被拒绝,而不是运行期报错。\n")
	fmt.Fprintf(&b, "type %sTable struct{}\n\n", sing)
	fmt.Fprintf(&b, "// %sUpdate 是 Update 方法族的命名空间对象(如 %sUpdate.SetName)。\n", sing, sing)
	b.WriteString("// 取 XxxUpdate 而非表名单数:与 sqlc-gen-go 同包时模型类型已占用单数名\n")
	b.WriteString("// (如 sys_user → SysUser),命名空间对象不能复用。\n")
	fmt.Fprintf(&b, "var %sUpdate %sTable\n\n", sing, sing)
	fmt.Fprintf(&b, "// %sUpdateOption 是本表的更新选项别名。SET 资格面是列级 @updatable 的\n", sing)
	b.WriteString("// set-capable 形态;WHERE 资格面是 where-capable 形态,有 @pk 时自动附带\n")
	b.WriteString("// 主键列的完整类型矩阵(定位方法)。两个能力面与 @dynamic / @scope 解耦。\n")
	fmt.Fprintf(&b, "type %sUpdateOption = dynq.UpdateOption[%sTable]\n\n", sing, sing)
	return b.String()
}

// ---- SET 方法族 ----

func (tc *tableCode) updateSetSection() string {
	sing := tc.spec.Singular
	var b strings.Builder
	b.WriteString("// ---- SET 白名单(set-capable @updatable 列;未调用的列保持原值)----\n\n")
	for _, c := range tc.spec.Updatables {
		typ := c.GoType
		if c.Nullable {
			typ = "*" + c.GoType
		}
		if label := setCommentLabel(c); label != "" {
			fmt.Fprintf(&b, "// Set%s 设置 %s（%s）列值", c.GoConst, c.Key, label)
		} else {
			fmt.Fprintf(&b, "// Set%s 设置 %s 列值", c.GoConst, c.Key)
		}
		if c.Nullable {
			b.WriteString("，nil = 显式置 NULL，字面量取指针用 dynq.Ptr")
		} else {
			b.WriteString("，无 nil")
		}
		b.WriteString("。\n")
		fmt.Fprintf(&b, "func (%sTable) Set%s(v %s) %sUpdateOption {\n", sing, c.GoConst, typ, sing)
		fmt.Fprintf(&b, "\treturn func(st *dynq.UpdateState[%sTable]) error {\n", sing)
		if !c.Nullable && strings.HasPrefix(c.GoType, "[]") {
			// 切片类型的 nil 会编码为 NULL;非空列必须运行期拒绝,
			// 使"非空列无 nil 通道"的签名承诺对切片同样成立。
			fmt.Fprintf(&b, "\t\tif v == nil {\n\t\t\treturn dynq.ErrNilSet(%q)\n\t\t}\n", c.Key)
		}
		fmt.Fprintf(&b, "\t\treturn st.Set(%q, %q, %q, v)\n", c.Key, c.Physical, c.whereCast())
		b.WriteString("\t}\n}\n\n")
	}
	return b.String()
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
	sing := tc.spec.Singular
	var b strings.Builder
	b.WriteString("// ---- @filter 绕过 ----\n\n")
	b.WriteString("// WithoutFilter 绕过本表的 @filter 不变量(幂等;不豁免零 where 守卫)。\n")
	b.WriteString("// 典型用途:恢复被软删不变量挡住的历史行;绕过是整表级开关,不做条件细化。\n")
	fmt.Fprintf(&b, "func (%sTable) WithoutFilter() %sUpdateOption {\n", sing, sing)
	fmt.Fprintf(&b, "\treturn func(st *dynq.UpdateState[%sTable]) error {\n", sing)
	b.WriteString("\t\tst.BypassFilter()\n\t\treturn nil\n\t}\n}\n\n")
	return b.String()
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
	var b strings.Builder
	b.WriteString("// ---- WHERE 定位方法族(@pk 自动矩阵 + where-capable @updatable)----\n\n")
	for _, f := range tc.updateWhereFields() {
		for _, op := range f.ops {
			name := "Where" + f.col.GoConst + updateOpSuffix[op]
			sqlDesc := updateOpSQL[op]
			if op == "in" && f.col.DataType == "int_array" {
				// 数组列的 in 是 && 重叠语义,注释不能写 IN。
				sqlDesc = "&& 重叠"
			}
			fmt.Fprintf(&b, "// %s 追加 %s 列 %s 条件;多条 WHERE 之间恒为 AND。\n",
				name, f.col.Key, sqlDesc)
			fmt.Fprintf(&b, "func (%sTable) %s(%s) %sUpdateOption {\n",
				sing, name, updateWhereSignature(f.col, op), sing)
			fmt.Fprintf(&b, "\treturn func(st *dynq.UpdateState[%sTable]) error {\n", sing)
			for _, line := range strings.Split(strings.TrimRight(updateWhereBody(f.col, op), "\n"), "\n") {
				b.WriteString("\t\t" + line + "\n")
			}
			b.WriteString("\t}\n}\n\n")
		}
	}
	return b.String()
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
	col := c.Physical
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
	sing, res := spec.Singular, tc.res
	table := tableNameQualified(spec.Name, tc.opts)
	var b strings.Builder
	fmt.Fprintf(&b, "// Update%s 按 opts 收集的 SET / WHERE 组合更新行,返回实际改写行数。\n", res)
	b.WriteString("// SET 白名单与 WHERE 定位条件均来自列级 @updatable;有 @pk 时自动附带\n")
	b.WriteString("// 主键列的定位方法(类型矩阵)。@scope 不参与写面,权限由读取阶段保证。\n")
	b.WriteString("// 零 SET 或零调用方 WHERE 条件显式拒绝(Kind=set / where),不发全表 UPDATE,\n")
	b.WriteString("// 也不静默 no-op。\n")
	if len(spec.Filters) > 0 {
		b.WriteString("// @filter 不变量恒定 AND 进 WHERE;WithoutFilter() 可整体绕过,但不豁免\n")
		b.WriteString("// 零 where 守卫。\n")
	}
	if !spec.ReadEnabled {
		b.WriteString("//\n// 本表无 @pk:零 where 守卫只保证\"至少一个条件\",条件可能命中多行;\n")
		b.WriteString("// 调用方必须自带唯一组合条件(如全部业务键列),否则会批量改写。\n")
	}
	b.WriteString("//\n")
	b.WriteString("// 参数:\n")
	b.WriteString("//   - ctx: 更新上下文。\n")
	b.WriteString("//   - db:  数据库连接,满足 DBTX 接口即可。\n")
	fmt.Fprintf(&b, "//   - opts: SET / WHERE 选项(%sUpdate.*),跨表选项编译期不可混用。\n", sing)
	b.WriteString("//\n")
	b.WriteString("// 返回值:\n")
	b.WriteString("//   - int64: 实际改写行数;0 = 未命中(条件不匹配),不隐式特判。\n")
	b.WriteString("//   - error: 校验或执行失败时的错误。\n")
	fmt.Fprintf(&b, "func Update%s(ctx context.Context, db %s, opts ...%sUpdateOption) (int64, error) {\n", res, tc.dbtx, sing)
	fmt.Fprintf(&b, "\tst := dynq.NewUpdateState[%sTable]()\n", sing)
	b.WriteString("\tfor _, opt := range opts {\n")
	b.WriteString("\t\tif opt == nil {\n\t\t\tcontinue\n\t\t}\n")
	b.WriteString("\t\tif err := opt(st); err != nil {\n\t\t\treturn 0, err\n\t\t}\n")
	b.WriteString("\t}\n")
	b.WriteString("\t// 空 SET / 零 where 守卫:按调用方条件数计——@filter 叠加后最终 WHERE\n")
	b.WriteString("\t// 恒非空,不得以最终 SQL 形态代替判断。\n")
	b.WriteString("\tif err := st.Validate(); err != nil {\n\t\treturn 0, err\n\t}\n")
	if len(spec.Filters) > 0 {
		fmt.Fprintf(&b, "\tconds := make([]sq.Sqlizer, 0, %d+len(st.Wheres()))\n", len(spec.Filters))
		b.WriteString("\t// 恒定条件:@filter 服务端不变量(WithoutFilter 可绕过),与调用方条件 AND。\n")
		b.WriteString("\tif !st.FilterBypassed() {\n")
		for i := range spec.Filters {
			fmt.Fprintf(&b, "\t\tconds = append(conds, %s)\n", tc.filterExpr(&spec.Filters[i]))
		}
		b.WriteString("\t}\n")
	} else {
		b.WriteString("\tconds := make([]sq.Sqlizer, 0, len(st.Wheres()))\n")
	}
	b.WriteString("\tconds = append(conds, st.Wheres()...)\n")
	fmt.Fprintf(&b, "\tb := sq.Update(%q).PlaceholderFormat(sq.Dollar).Where(sq.And(conds))\n", table)
	b.WriteString("\tfor _, a := range st.Sets() {\n")
	b.WriteString("\t\tb = b.Set(a.Column, sq.Expr(\"?\"+a.Cast, a.Value))\n")
	b.WriteString("\t}\n")
	b.WriteString("\tquery, args, err := b.ToSql()\n")
	b.WriteString("\tif err != nil {\n\t\treturn 0, err\n\t}\n")
	b.WriteString("\ttag, err := db.Exec(ctx, query, args...)\n")
	b.WriteString("\tif err != nil {\n\t\treturn 0, err\n\t}\n")
	b.WriteString("\treturn tag.RowsAffected(), nil\n")
	b.WriteString("}\n")
	return b.String()
}
