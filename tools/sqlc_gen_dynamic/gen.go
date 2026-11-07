package main

import (
	"context"
	"fmt"
	"go/format"
	"os"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// gen.go:TableSpec → 生成代码的入口与读面装配。
// 静态骨架见 templates.go,逐列 / 逐 op 的差异由片段函数填装,写入原语见
// codegen.go;生成结果经 go/format 归一。

// Generate 是插件入口(sqlc 经 stdin/stdout 调用)。
func Generate(_ context.Context, req *pb.GenerateRequest) (*pb.GenerateResponse, error) {
	// dump-first(§4.6):DYNQ_DUMP_REQUEST=path 时把请求 protojson 落盘后退出,
	// 用于验证 COMMENT ON 是否进入 columns[].comment、类型 oneof 真实形态、
	// enum 值列表是否完整,并作为 golden 测试的真实夹具。
	if path := os.Getenv("DYNQ_DUMP_REQUEST"); path != "" {
		out, err := protojson.MarshalOptions{Multiline: true}.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("dump request: %w", err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return nil, fmt.Errorf("dump request: %w", err)
		}
	}
	opts, err := ParseOptions(req)
	if err != nil {
		return nil, err
	}
	tables, notices, err := buildTableSpecs(req, opts)
	if err != nil {
		return nil, err
	}
	// 方言门:本插件产物绑定 PostgreSQL + pgx/v5——读面用 $N 占位符与 pgx 收行,
	// 更新面另有 RETURNING,且 dynq 运行时依赖 pgtype。engine 非 postgresql 时
	// 在生成期显式报错:读面与更新面同一门槛,不静默产出跑不起来的方言。
	// sql_package 不进插件请求(见 Settings proto),无法二次校验,故文档写死
	// pgx/v5 前置条件。
	if engine := req.GetSettings().GetEngine(); engine != "postgresql" {
		if len(tables) > 0 {
			return nil, fmt.Errorf("table %s: engine %q is not supported (this plugin generates PostgreSQL dialect with pgx/v5 only); set engine: postgresql in sqlc.yaml", tables[0].Name, engine)
		}
	}
	// 生成期告警(stderr,不进 stdout——stdout 是 sqlc 协议通道):弃用旧名
	// 提示与注解无效告警,每条自带 "deprecation: "/"warning: " 分类前缀。
	// 非 fail:旧名解析为等价能力,提示指向新名与计划移除版本,见 deprecatedDirectives。
	for _, n := range notices {
		fmt.Fprintf(os.Stderr, "sqlc_gen_dynamic: %s\n", n)
	}
	resp := &pb.GenerateResponse{}
	for _, spec := range tables {
		src, err := renderTable(spec, opts)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", spec.Name, err)
		}
		resp.Files = append(resp.Files, &pb.File{
			Name:     spec.Name + "_dynamic_gen.go",
			Contents: src,
		})
	}
	return resp, nil
}

// ---- 生成上下文 ----

type tableCode struct {
	spec *TableSpec
	opts *Options

	modelPkg string // 模型类型限定前缀("" 同包 / "models." 跨包)
	dbtx     string // DBTX 引用
	res      string // 资源 PascalCase(如 Users)
	sing     string // 单数实体 PascalCase(如 User)
	reg      string // registry 变量名前缀(如 users)
}

func renderTable(spec *TableSpec, opts *Options) ([]byte, error) {
	tc := &tableCode{spec: spec, opts: opts}
	if opts.ModelImport != "" {
		tc.modelPkg = opts.ModelPackage + "."
	}
	tc.dbtx = "DBTX"
	if opts.ModelImport != "" {
		tc.dbtx = tc.modelPkg + "DBTX"
	}
	tc.res = goName(spec.Resource)
	tc.sing = spec.Singular
	tc.reg = lowerFirst(spec.Name)

	// 读面(@pk)与更新面独立拼装:无 @pk 表只产 header + update 段
	// (registry / Params / Parse / List / Count / Schema / 游标一律不生成)。
	header, err := tc.headerSection()
	if err != nil {
		return nil, err
	}
	sections := []string{header}
	if spec.ReadEnabled {
		sections = append(sections,
			tc.registrySection(),
			tc.fieldConstSection(),
			tc.paramsSection(),
			tc.parseSection(),
			tc.validateSection(),
			tc.whereSection(),
			tc.cursorValuesSection(),
			tc.listSection(),
			tc.countSection(),
			tc.updateSection(),
			tc.schemaSection(),
		)
	} else {
		sections = append(sections, tc.updateSection())
	}
	var p printer
	for _, s := range sections {
		p.Raw(s)
		p.Blank()
	}
	src, err := format.Source([]byte(p.String()))
	if err != nil {
		return nil, fmt.Errorf("gofmt generated source: %w\n--- source ---\n%s", err, p.String())
	}
	return src, nil
}

func (tc *tableCode) headerSection() (string, error) {
	spec := tc.spec
	var p printer
	p.Raw(headerPrefix)
	for _, d := range spec.TableDirectives {
		p.Linef("//   table %s: [%s]", spec.Name, d)
	}
	for _, c := range spec.Columns {
		if len(c.Directives) == 0 {
			continue
		}
		p.Linef("//   column %s.%s: [%s]", spec.Name, c.Physical, strings.Join(c.Directives, " "))
	}
	p.Raw(fill(headerSuffix, "pkg", tc.opts.Package))
	imports, err := tc.importsSection()
	if err != nil {
		return "", err
	}
	p.Raw(imports)
	return p.String(), nil
}

func (tc *tableCode) importsSection() (string, error) {
	spec := tc.spec
	// time 引用来源:读面 Params(*time.Time / []time.Time)、Set 方法(*T)、
	// Where 方法(value op)与 @pk 自动定位方法(类型矩阵)。
	// 仅 is_null/not_null 的列只产 bool 字段,不引用列 GoType。
	needTime := false
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if !strings.Contains(c.GoType, "time.Time") {
			continue
		}
		if spec.ReadEnabled && hasValueOp(c.Ops) {
			needTime = true
		}
		// Set / Where 方法族与 @pk 自动定位矩阵只在更新面启用时生成;
		// 只读表(无 set-capable 或 @update: false)不得因此引入无用 import。
		if spec.UpdateEnabled && (c.SetCapable || hasValueOp(c.UpdateOps) || (spec.ReadEnabled && c.Key == spec.Tiebreaker)) {
			needTime = true
		}
	}
	needJSON := false
	for _, p := range spec.ScopeParams {
		if p.GoType == "json.RawMessage" {
			needJSON = true
		}
	}
	// 固定 import 的别名占位:override 别名不得与本文件已发射的别名重复。
	// Go import 别名是文件作用域,同文件重复声明产物不可编译;别名落在不同
	// 表的文件中则各自合法(如两表分别引用同名包 types 的不同来源)。
	fixedAliases := map[string]string{
		"context": "context",
		"sq":      "github.com/Masterminds/squirrel",
		"dynq":    tc.opts.RuntimeImport,
	}
	if needJSON {
		fixedAliases["json"] = "encoding/json"
	}
	if needTime {
		fixedAliases["time"] = "time"
	}
	// pgx 引用来源:读面收行(CollectRows)与更新面 RETURNING 收行(多行
	// CollectRows / 单行 CollectOneRow)。
	if spec.ReadEnabled || spec.UpdateEnabled {
		fixedAliases["pgx"] = "github.com/jackc/pgx/v5"
	}
	if tc.opts.ModelImport != "" {
		fixedAliases[tc.opts.ModelPackage] = tc.opts.ModelImport
	}
	// go_type_overrides 的外部包:仅收集在生成面中实际被引用的列的声明,
	// 未引用的 import 会让产物不可编译。引用面只有更新面:Set 签名、Where
	// 签名(值/数组 op)与 pk 自动定位矩阵。读面 Params 槽位虽也引用 GoType,
	// 但带 import 的分叉 override + 读面值 op 已被 validateTable 拒绝,非
	// 分叉组合只可能是与 models 同包的枚举同型(上方已 continue)——读面
	// 不会成为唯一引用面,无需参与判定。
	overrideImports := map[string]string{} // import 路径 → 包别名
	type aliasOrigin struct {              // 别名 → 首个引用列,撞名时报错定位
		path   string
		column string
	}
	aliasOrigins := map[string]aliasOrigin{}
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if c.OverrideImport == nil {
			continue
		}
		o := c.OverrideImport.GoType
		if o.Import == tc.opts.ModelImport {
			continue // 与 models 同包:import 已存在,别名重复声明会撞名
		}
		used := spec.UpdateEnabled && (c.SetCapable || opsUseGoType(c.UpdateOps, goTypeOpUpdate) ||
			(spec.ReadEnabled && c.Key == spec.Tiebreaker && opsUseGoType(matrixOps(c.DataType), goTypeOpUpdate)))
		if !used {
			continue
		}
		if prev, ok := aliasOrigins[o.Package]; ok {
			if prev.path == o.Import {
				continue // 同路径同别名(如 db_type 形态命中多列):去重即可
			}
			return "", fmt.Errorf("column %s: go_type_overrides alias %q maps to import %q but is already bound to %q (column %s); distinct imports cannot share one alias in one generated file — rename the package of one override",
				c.Physical, o.Package, o.Import, prev.path, prev.column)
		}
		if fixedPath, ok := fixedAliases[o.Package]; ok {
			return "", fmt.Errorf("column %s: go_type_overrides alias %q collides with the generated import %q; rename the override package",
				c.Physical, o.Package, fixedPath)
		}
		overrideImports[o.Import] = o.Package
		aliasOrigins[o.Package] = aliasOrigin{path: o.Import, column: c.Physical}
	}
	var std, rest []string
	std = append(std, `"context"`)
	if needJSON {
		std = append(std, `"encoding/json"`)
	}
	if needTime {
		std = append(std, `"time"`)
	}
	rest = append(rest, `sq "github.com/Masterminds/squirrel"`)
	if spec.ReadEnabled || spec.UpdateEnabled {
		rest = append(rest, `"github.com/jackc/pgx/v5"`)
	}
	if tc.opts.ModelImport != "" {
		rest = append(rest, fmt.Sprintf("%s %q", tc.opts.ModelPackage, tc.opts.ModelImport))
	}
	for path, pkg := range overrideImports {
		rest = append(rest, fmt.Sprintf("%s %q", pkg, path))
	}
	rest = append(rest, fmt.Sprintf("dynq %q", tc.opts.RuntimeImport))
	sort.Strings(std)
	sort.Strings(rest)
	var p printer
	p.Line("import (")
	for _, l := range std {
		p.Linef("\t%s", l)
	}
	p.Blank()
	for _, l := range rest {
		p.Linef("\t%s", l)
	}
	p.Line(")")
	p.Blank()
	return p.String(), nil
}

// ---- registry 与字段常量 ----

var dataTypeConst = map[string]string{
	"integer": "dynq.DataTypeInteger", "number": "dynq.DataTypeNumber",
	"decimal":  "dynq.DataTypeDecimal",
	"datetime": "dynq.DataTypeDatetime", "date": "dynq.DataTypeDate",
	"string": "dynq.DataTypeString", "boolean": "dynq.DataTypeBoolean",
	"uuid": "dynq.DataTypeUUID", "enum": "dynq.DataTypeEnum",
	"jsonb": "dynq.DataTypeJSONB", "int_array": "dynq.DataTypeIntArray",
}

var opConst = map[string]string{
	"eq": "dynq.OpEq", "neq": "dynq.OpNeq", "gt": "dynq.OpGt", "gte": "dynq.OpGte",
	"lt": "dynq.OpLt", "lte": "dynq.OpLte", "between": "dynq.OpBetween",
	"in": "dynq.OpIn", "not_in": "dynq.OpNotIn", "prefix": "dynq.OpPrefix",
	"suffix": "dynq.OpSuffix", "contains": "dynq.OpContains",
	"is_null": "dynq.OpIsNull", "not_null": "dynq.OpNotNull", "has": "dynq.OpHas",
}

func (tc *tableCode) registrySection() string {
	spec := tc.spec
	var p printer
	p.Raw(fill(registryDoc,
		"reg", tc.reg,
		"resource", strconv.Quote(spec.Resource),
		"table", strconv.Quote(tableNameQualified(spec.Name, tc.opts)),
	))
	for _, c := range spec.Columns {
		p.Line("\t\t{")
		p.Linef("\t\t\tKey:        %q,", c.Key)
		p.Linef("\t\t\tColumn:     %q,", c.SQLName)
		p.Linef("\t\t\tLabel:      %q,", c.Label)
		p.Linef("\t\t\tDataType:   %s,", dataTypeOrRaw(c.DataType))
		p.Linef("\t\t\tNullable:   %v,", c.Nullable)
		if len(c.Ops) > 0 {
			ops := make([]string, len(c.Ops))
			for i, op := range c.Ops {
				ops[i] = opConst[op]
			}
			p.Linef("\t\t\tOps:        []dynq.Op{%s},", strings.Join(ops, ", "))
		}
		p.Linef("\t\t\tSortable:   %v,", c.Sortable)
		p.Linef("\t\t\tSelectable: %v,", c.Selectable)
		p.Linef("\t\t\tHidden:     %v,", c.Hidden)
		if len(c.EnumVals) > 0 {
			p.Linef("\t\t\tEnum:       []string{%s},", quoteJoin(c.EnumVals))
		}
		if c.Cast() != "" {
			p.Linef("\t\t\tCast:       %q,", c.Cast())
		}
		if c.API != nil {
			p.Line("\t\t\tAPI: &dynq.FieldAPI{")
			p.Linef("\t\t\t\tEndpoint: %q,", c.API.Endpoint)
			p.Linef("\t\t\t\tValueKey: %q,", c.API.ValueKey)
			p.Linef("\t\t\t\tLabelKey: %q,", c.API.LabelKey)
			p.Linef("\t\t\t\tMode:     %q,", c.API.Mode)
			p.Line("\t\t\t},")
		}
		// 展示注解(@label 已并入 Label;@desc/@order/@options 按声明输出)。
		if c.Desc != "" {
			p.Linef("\t\t\tDesc:       %q,", c.Desc)
		}
		if c.Order != nil {
			p.Linef("\t\t\tOrder:      dynq.IntPtr(%d),", *c.Order)
		}
		if len(c.Options) > 0 {
			p.Line("\t\t\tOptions:    []dynq.FieldOption{")
			for _, o := range c.Options {
				switch v := o.Value.(type) {
				case int64:
					p.Linef("\t\t\t\t{Value: int64(%d), Label: %q},", v, o.Label)
				case string:
					p.Linef("\t\t\t\t{Value: %q, Label: %q},", v, o.Label)
				default:
					// 不可达:parseOptionsDirective 保证 value 仅 int64/string。
					panic(fmt.Sprintf("column %s: unsupported @options value type %T", c.Physical, o.Value))
				}
			}
			p.Line("\t\t\t},")
		}
		p.Line("\t\t},")
	}
	p.Line("\t},")
	if len(spec.DefaultSort) > 0 {
		p.Line("\tDefaultSort: []dynq.OrderBy{")
		for _, ds := range spec.DefaultSort {
			p.Linef("\t\t{Field: %q, Desc: %v},", camelKey(ds.Column), ds.Desc)
		}
		p.Line("\t},")
	}
	p.Linef("\tTiebreaker:  %q,", spec.Tiebreaker)
	p.Linef("\tPageDefault: %d,", spec.PageDefault)
	p.Linef("\tPageMax:     %d,", spec.PageMax)
	p.Line("}")
	p.Blank()

	p.Raw(fill(fieldIndexDoc, "reg", tc.reg))
	for i, c := range spec.Columns {
		p.Linef("\t%q: %d,", c.Key, i)
	}
	p.Line("}")

	for _, c := range spec.Columns {
		if len(c.EnumVals) == 0 || len(c.Ops) == 0 {
			continue
		}
		p.Raw(fill(enumValsDoc,
			"reg", tc.reg,
			"const", c.GoConst,
			"key", c.Key,
			"list", "{"+quoteJoin(c.EnumVals)+"}",
		))
	}
	return p.String()
}

// quoteJoin 返回逐项 Go 字符串字面量、逗号分隔的列表。
func quoteJoin(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return strings.Join(quoted, ", ")
}

func dataTypeOrRaw(dt string) string {
	if c, ok := dataTypeConst[dt]; ok {
		return c
	}
	return strconv.Quote(dt)
}

func (tc *tableCode) fieldConstSection() string {
	spec := tc.spec
	var decls, vals printer
	for _, c := range spec.Columns {
		if c.Hidden {
			continue
		}
		decls.Linef("\t%s dynq.FieldName", c.GoConst)
	}
	for _, c := range spec.Columns {
		if c.Hidden {
			continue
		}
		vals.Linef("\t%s: %q,", c.GoConst, c.Key)
	}
	var p printer
	p.Raw(fill(fieldConstSkel,
		"sing", spec.Singular,
		"decls", decls.String(),
		"vals", vals.String(),
	))
	return p.String()
}

// ---- Params ----

// opSuffix:op → Params 字段后缀与目标槽位后缀。
var opSuffix = map[string]string{
	"eq": "", "neq": "Neq", "in": "In", "not_in": "NotIn",
	"gt": "Gt", "gte": "Gte", "lt": "Lt", "lte": "Lte",
	"contains": "Contains", "prefix": "Prefix", "suffix": "Suffix",
	"is_null": "IsNull", "not_null": "NotNull", "has": "Has",
}

func (tc *tableCode) paramsSection() string {
	spec := tc.spec
	var p printer
	if len(spec.Scope) > 0 {
		var decls printer
		for _, sp := range spec.ScopeParams {
			decls.Linef("\t%s %s", sp.GoConst, sp.GoType)
		}
		p.Raw(fill(scopeStructSkel, "res", tc.res, "decls", decls.String()))
	}
	p.Raw(fill(paramsHead, "res", tc.res))

	var fields printer
	for _, c := range spec.Columns {
		if c.Hidden {
			continue
		}
		seen := map[string]bool{}
		emit := func(name, typ string) {
			if seen[name] {
				return // between 拆分与独立 gte/lte 共享目标字段,去重
			}
			seen[name] = true
			fields.Linef("\t%s %s", name, typ)
		}
		sliceType := "[]" + c.GoType
		if strings.HasPrefix(c.GoType, "[]") {
			sliceType = c.GoType // int_array 列的 GoType 已是切片,不再包一层
		}
		for _, op := range orderedOps(c.Ops) {
			switch op {
			case "between":
				emit(fmt.Sprintf("%sGte", c.GoConst), "*"+c.GoType)
				emit(fmt.Sprintf("%sLte", c.GoConst), "*"+c.GoType)
			case "eq":
				emit(c.GoConst, "*"+c.GoType)
			case "neq", "gt", "gte", "lt", "lte":
				emit(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), "*"+c.GoType)
			case "in", "not_in":
				emit(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), sliceType)
			case "contains", "prefix", "suffix":
				emit(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), "string")
			case "is_null", "not_null":
				emit(fmt.Sprintf("%s%s", c.GoConst, opSuffix[op]), "bool")
			case "has":
				emit(fmt.Sprintf("%sHas", c.GoConst), c.GoType)
			}
		}
	}
	p.Raw(fields.String())
	if len(spec.Scope) > 0 {
		p.Raw(fill(paramsScopeFields, "res", tc.res))
	}
	p.Raw(paramsTail)
	return p.String()
}

// orderedOps 按 canonical 顺序返回 ops。
func orderedOps(ops []string) []string {
	var out []string
	for _, c := range canonicalOpOrder {
		for _, op := range ops {
			if op == c {
				out = append(out, op)
				break
			}
		}
	}
	return out
}

// hasValueOp 报告 ops 是否含携带值的操作符(is_null/not_null 仅产 bool 槽位)。
func hasValueOp(ops []string) bool {
	for _, op := range ops {
		switch op {
		case "is_null", "not_null":
		default:
			return true
		}
	}
	return false
}

// goTypeOpRead / goTypeOpUpdate 划分各 op 的生成签名是否引用列 GoType,供
// go_type_overrides 的 import 收集判定(未引用的 import 会让产物不可编译)。
// 两面发射形态不同,必须分开判定:
//   - 读面 Params 槽位:值 op / 数组 op 引用(has 字段类型即 GoType);
//     模糊三 op 恒 string、is_null/not_null 恒 bool;
//   - 更新面 Where 方法签名:值 op / 数组 op 引用;模糊三 op 恒 string、
//     has 恒 []byte(has 参数须为 JSON 文档字节,不参与 override 类型)、
//     is_null/not_null 无参。Set 方法恒引用(SetCapable 由调用方单独判定)。
func goTypeOpRead(op string) bool {
	switch op {
	case "contains", "prefix", "suffix", "is_null", "not_null":
		return false
	default:
		return true
	}
}

func goTypeOpUpdate(op string) bool {
	switch op {
	case "eq", "neq", "gt", "gte", "lt", "lte", "between", "in", "not_in":
		return true
	default:
		return false
	}
}

// opsUseGoType 按面谓词报告 ops 是否存在引用列 GoType 的生成签名。
func opsUseGoType(ops []string, refs func(string) bool) bool {
	for _, op := range ops {
		if refs(op) {
			return true
		}
	}
	return false
}

// coerceCall 返回标量收敛表达式(不含错误处理)。
func (tc *tableCode) coerceCall(c *ColumnSpec, valueExpr string, right bool) (call string, resultVar string) {
	src := "c.Value"
	if right {
		src = "c.RightValue"
	}
	_ = valueExpr
	switch c.DataType {
	case "integer":
		return "dynq.AsInt64(" + src + ")", "v"
	case "number", "decimal":
		return "dynq.AsFloat64(" + src + ")", "v"
	case "datetime":
		return "dynq.AsTime(" + src + ")", "v"
	case "date":
		return "dynq.AsDate(" + src + ")", "v"
	case "string":
		return "dynq.AsString(" + src + ")", "v"
	case "boolean":
		return "dynq.AsBool(" + src + ")", "v"
	case "uuid":
		return "dynq.AsUUID(" + src + ")", "v"
	case "enum":
		return "dynq.AsEnum(" + tc.reg + c.GoConst + "EnumVals, " + src + ")", "v"
	case "jsonb":
		return "dynq.AsJSON(" + src + ")", "v"
	case "int_array":
		return "dynq.AsInt64Slice(" + src + ")", "vs"
	default: // unmapped:仅可 is_null/not_null,无标量收敛
		return "", ""
	}
}

func (tc *tableCode) coerceSliceCall(c *ColumnSpec) string {
	switch c.DataType {
	case "integer", "int_array":
		return "dynq.AsInt64Slice(c.Value)"
	case "number", "decimal":
		return "dynq.AsFloat64Slice(c.Value)"
	case "datetime":
		return "dynq.AsTimeSlice(c.Value)"
	case "date":
		return "dynq.AsDateSlice(c.Value)"
	case "string":
		return "dynq.AsStringSlice(c.Value)"
	case "boolean":
		return "dynq.AsBoolSlice(c.Value)"
	case "uuid":
		return "dynq.AsUUIDSlice(c.Value)"
	case "enum":
		return "dynq.AsEnumSlice(" + tc.reg + c.GoConst + "EnumVals, c.Value)"
	default:
		return ""
	}
}

// takeLine 生成槽位占用语句。
func takeLine(c *ColumnSpec, slot string) string {
	return fmt.Sprintf("if err := slots.Take(%q, i, %q); err != nil {\n\t\t\t\t\treturn out, err\n\t\t\t\t}", c.Key+"."+slot, c.Key)
}

func coerceErrLine(c *ColumnSpec) string {
	return fmt.Sprintf("if cerr != nil {\n\t\t\t\t\treturn out, dynq.AsError(cerr).At(dynq.KindWhere, i, %q)\n\t\t\t\t}", c.Key)
}

// parseOpCase 生成单个 (字段, op) 的 case 体。
func (tc *tableCode) parseOpCase(c *ColumnSpec, op string) string {
	var p printer
	p.Linef("\t\t\tcase %s:", opConst[op])
	p.Indented("\t\t\t\t", tc.parseOpBody(c, op))
	return p.String()
}

func (tc *tableCode) parseOpBody(c *ColumnSpec, op string) string {
	var b strings.Builder
	slot := func(s string) { b.WriteString(takeLine(c, s) + "\n") }
	coerce := func(right bool) string {
		call, varName := tc.coerceCall(c, "", right)
		fmt.Fprintf(&b, "%s, cerr := %s\n", varName, call)
		b.WriteString(coerceErrLine(c) + "\n")
		return varName
	}
	switch op {
	case "eq", "neq":
		slot(op)
		v := coerce(false)
		if c.DataType == "enum" {
			fmt.Fprintf(&b, "out.%s%s = dynq.Addr(%s(%s))\n", c.GoConst, opSuffix[op], c.EnumGoType, v)
		} else {
			fmt.Fprintf(&b, "out.%s%s = &%s\n", c.GoConst, opSuffix[op], v)
		}
	case "in", "not_in":
		slot(op)
		fmt.Fprintf(&b, "vs, cerr := %s\n", tc.coerceSliceCall(c))
		b.WriteString(coerceErrLine(c) + "\n")
		b.WriteString("if err := budget.Add(len(vs)); err != nil {\n\t\t\t\t\treturn out, dynq.AsError(err).At(dynq.KindWhere, i, \"" + c.Key + "\")\n\t\t\t}\n")
		if c.DataType == "enum" {
			fmt.Fprintf(&b, "out.%s%s = dynq.ConvertSlice[%s](vs)\n", c.GoConst, opSuffix[op], c.EnumGoType)
		} else {
			fmt.Fprintf(&b, "out.%s%s = vs\n", c.GoConst, opSuffix[op])
		}
	case "gt", "gte", "lt", "lte":
		slot(op)
		v := coerce(false)
		fmt.Fprintf(&b, "out.%s%s = &%s\n", c.GoConst, opSuffix[op], v)
	case "between":
		slot("gte")
		slot("lte")
		lv := func() string {
			call, _ := tc.coerceCall(c, "", false)
			b.WriteString("lv, cerr := " + call + "\n")
			b.WriteString(coerceErrLine(c) + "\n")
			return "lv"
		}()
		rv := func() string {
			call, _ := tc.coerceCall(c, "", true)
			b.WriteString("rv, cerr := " + call + "\n")
			b.WriteString(coerceErrLine(c) + "\n")
			return "rv"
		}()
		fmt.Fprintf(&b, "out.%sGte = &%s\nout.%sLte = &%s\n", c.GoConst, lv, c.GoConst, rv)
	case "contains", "prefix", "suffix":
		slot(op)
		v := coerce(false)
		fmt.Fprintf(&b, "out.%s%s = %s\n", c.GoConst, opSuffix[op], v)
	case "is_null", "not_null":
		slot(op)
		fmt.Fprintf(&b, "out.%s%s = true\n", c.GoConst, opSuffix[op])
	case "has":
		slot(op)
		v := coerce(false)
		fmt.Fprintf(&b, "out.%sHas = %s\n", c.GoConst, v)
	}
	return b.String()
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func tableNameQualified(name string, opts *Options) string {
	if opts.Schema != "" && opts.Schema != "public" {
		return sqlIdent(opts.Schema) + "." + sqlIdent(name)
	}
	return sqlIdent(name)
}
