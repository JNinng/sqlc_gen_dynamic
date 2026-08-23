package main

import (
	"fmt"
	"strings"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// catalog.go:CodeGenRequest.Catalog → TableSpec(生成期元数据 + 全部生成期校验)。

// ColumnSpec 是一列的生成期规格。
type ColumnSpec struct {
	Physical   string
	Key        string // camelCase 逻辑键
	GoConst    string // 字段常量结构体成员名(PascalCase)
	ModelField string // sqlc-gen-go 模型结构体字段名(游标取值用)
	Label      string
	DataType   string // dynq.DataType 值;"unmapped" 表示不可过滤/排序仅可投影
	GoType     string // Params Go 类型(已含模型包限定前缀)
	Nullable   bool
	Ops        []string
	Sortable   bool
	Selectable bool
	Hidden     bool
	EnumVals   []string
	EnumGoType string // 同包模型枚举类型名(如 UserStatus);跨包时已含前缀
	// EnumPhysical 是 PG 枚举类型名(如 user_status),用于占位符显式 cast。
	EnumPhysical string
	NumericCast  bool
	// API 是 @api 选项数据源声明(nil = 无)。
	API *APISpec
	// DSL 回显(文件头策略来源)。
	Directives []string
}

// TableSpec 是一张表的生成期规格。
type TableSpec struct {
	Name            string // 物理表名
	Resource        string // 资源名(= 表名)
	ModelName       string // sqlc-gen-go 模型结构体名(如 User)
	Singular        string // 单数实体名(如 User)
	Columns         []ColumnSpec
	DefaultSort     []sortDirective
	Tiebreaker      string // 逻辑键
	PageDefault     int
	PageMax         int
	TableDirectives []string
	Scope           []scopeGuard  // @scope 守卫声明;空 = 未声明
	ScopeParams     []scopeParam  // 守卫标识符参数的并集(声明序),驱动 Scope 结构体
}

// scopeParam 是 Scope 结构体的一个字段来源:DSL 参数名 + Go 类型。
type scopeParam struct {
	Name    string
	GoConst string
	GoType  string
}

type sortDirective struct {
	Column string // 物理列名
	Desc   bool
}

// buildTableSpecs 从 Catalog 构建生成范围(§5.1/§5.3)。
func buildTableSpecs(req *pb.GenerateRequest, opts *Options) ([]*TableSpec, error) {
	schema := findSchema(req.Catalog, opts.Schema)
	if schema == nil {
		return nil, fmt.Errorf("schema %q not found in catalog", opts.Schema)
	}
	enums := enumMap(schema)

	// 枚举名 → 值列表
	// 逐表解析注释 DSL。
	byName := map[string]*TableSpec{}
	var order []*TableSpec
	for _, table := range schema.Tables {
		spec, included, err := buildTable(table, schema.Name, opts, enums)
		if err != nil {
			return nil, err
		}
		if !included {
			continue
		}
		byName[table.Rel.Name] = spec
		order = append(order, spec)
	}

	// 生成范围过滤。
	if len(opts.Tables) > 0 {
		var picked []*TableSpec
		for _, want := range opts.Tables {
			spec, ok := byName[want]
			if !ok {
				return nil, fmt.Errorf("table %q listed in options not found in schema %q (or it has no dynamic declarations and no @pk)", want, opts.Schema)
			}
			picked = append(picked, spec)
		}
		order = picked
	}

	for _, spec := range order {
		if err := validateTable(spec); err != nil {
			return nil, err
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("no tables with @dynamic declarations found in schema %q (annotate columns with [@dynamic: ...] or list tables in options)", opts.Schema)
	}
	return order, nil
}

func findSchema(catalog *pb.Catalog, name string) *pb.Schema {
	if catalog == nil {
		return nil
	}
	for _, s := range catalog.Schemas {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func enumMap(schema *pb.Schema) map[string][]string {
	m := map[string][]string{}
	for _, e := range schema.Enums {
		m[e.Name] = e.Vals
	}
	return m
}

// buildTable 解析单表;included=false 表示该表无动态声明且未被点名。
func buildTable(table *pb.Table, schemaName string, opts *Options, enums map[string][]string) (*TableSpec, bool, error) {
	tableWhere := fmt.Sprintf("table %s", table.Rel.Name)
	_, tableTags, err := parseComment(tableWhere, table.Comment)
	if err != nil {
		return nil, false, err
	}
	tableTags = mergeTags(tableTags)
	if err := validateTagNames(tableWhere, "table", tableTags); err != nil {
		return nil, false, err
	}

	spec := &TableSpec{
		Name:        table.Rel.Name,
		Resource:    table.Rel.Name,
		ModelName:   modelName(table.Rel.Name),
		Singular:    modelName(table.Rel.Name),
		PageDefault: opts.PageDefault,
		PageMax:     opts.PageMax,
	}

	hasDynamicDecl := false
	for _, col := range table.Columns {
		colWhere := fmt.Sprintf("column %s.%s", table.Rel.Name, col.Name)
		label, tags, err := parseComment(colWhere, col.Comment)
		if err != nil {
			return nil, false, err
		}
		tags = mergeTags(tags)
		if err := validateTagNames(colWhere, "column", tags); err != nil {
			return nil, false, err
		}

		c := ColumnSpec{
			Physical:   col.Name,
			Key:        camelKey(col.Name),
			GoConst:    goName(col.Name),
			ModelField: goName(col.Name),
			Label:      label,
			Nullable:   !col.NotNull,
			GoType:     "unmapped",
			DataType:   "unmapped",
		}
		if c.Label == "" {
			c.Label = col.Name
		}

		dynTag, hasDyn := findTag(tags, "dynamic")
		_, sortableTag := findTag(tags, "sortable")
		_, hiddenTag := findTag(tags, "hidden")
		_, selectableTag := findTag(tags, "selectable")
		_, hasAPI := findTag(tags, "api")

		// @hidden 与任一正向标记组合 → 生成期 fail。
		if hiddenTag && (hasDyn || sortableTag || selectableTag || hasAPI) {
			return nil, false, fmt.Errorf("%s: @hidden cannot be combined with @dynamic/@sortable/@selectable/@api", colWhere)
		}
		c.Hidden = hiddenTag
		c.Sortable = sortableTag
		c.Selectable = selectableTag

		dataType, goType, err := resolveType(colWhere, col, enums, opts, &c)
		if err != nil {
			return nil, false, err
		}
		c.DataType = dataType
		c.GoType = goType

		// @dynamic:显式声明或 default_ops 策略。
		if hasDyn {
			hasDynamicDecl = true
			ops, err := expandDynamicItems(colWhere, dataType, dynTag.Payload, c.Nullable)
			if err != nil {
				return nil, false, err
			}
			c.Ops = ops
			c.Directives = append(c.Directives, fmt.Sprintf("@dynamic: %s", strings.Join(ops, ", ")))
		} else if !c.Hidden && opts.DefaultOps != "none" && dataType != "unmapped" {
			ops, err := expandDynamicItems(colWhere, dataType, opts.DefaultOps, c.Nullable)
			if err != nil {
				return nil, false, err
			}
			c.Ops = ops
		}
		for _, name := range []string{"sortable", "hidden", "selectable"} {
			if t, ok := findTag(tags, name); ok {
				if t.Payload != "" {
					return nil, false, fmt.Errorf("%s: @%s takes no payload, got %q", colWhere, name, t.Payload)
				}
				c.Directives = append(c.Directives, "@"+name)
			}
		}
		// @api:选项数据源声明;仅 integer/uuid/string 引用字段支持。
		if t, ok := findTag(tags, "api"); ok {
			api, err := parseAPIDirective(colWhere, t)
			if err != nil {
				return nil, false, err
			}
			switch dataType {
			case "integer", "uuid", "string":
			default:
				return nil, false, fmt.Errorf("%s: @api not supported for dataType %s (want integer|uuid|string reference fields)", colWhere, dataType)
			}
			c.API = api
			directive := fmt.Sprintf("@api: %s value_key=%s label_key=%s", api.Endpoint, api.ValueKey, api.LabelKey)
			if api.Mode != "" {
				directive += " mode=" + api.Mode
			}
			c.Directives = append(c.Directives, directive)
		}
		// 未标注列不参与过滤能力生成(default_ops=none),但可投影、可被点名。
		spec.Columns = append(spec.Columns, c)
	}

	// 表级指令。
	if t, ok := findTag(tableTags, "page"); ok {
		def, max, err := parsePageDirective(tableWhere, t)
		if err != nil {
			return nil, false, err
		}
		spec.PageDefault, spec.PageMax = def, max
		spec.TableDirectives = append(spec.TableDirectives, fmt.Sprintf("@page: %d/%d", def, max))
	}
	if t, ok := findTag(tableTags, "default_sort"); ok {
		column, desc, err := parseDefaultSortDirective(tableWhere, t)
		if err != nil {
			return nil, false, err
		}
		spec.DefaultSort = append(spec.DefaultSort, sortDirective{Column: column, Desc: desc})
		spec.TableDirectives = append(spec.TableDirectives, fmt.Sprintf("@default_sort: %s %s", column, dirName(desc)))
	}
	if t, ok := findTag(tableTags, "pk"); ok {
		if t.Payload == "" || strings.ContainsAny(t.Payload, " /") {
			return nil, false, fmt.Errorf("%s: @pk wants a single column name, got %q", tableWhere, t.Payload)
		}
		spec.Tiebreaker = camelKey(t.Payload)
		spec.TableDirectives = append(spec.TableDirectives, "@pk: "+t.Payload)
	}
	if t, ok := findTag(tableTags, "scope"); ok {
		guards, err := parseScopeDirective(tableWhere, t)
		if err != nil {
			return nil, false, err
		}
		spec.Scope = guards
		spec.TableDirectives = append(spec.TableDirectives, "@scope: "+scopeEcho(guards))
		params, err := scopeParamsOf(tableWhere, guards)
		if err != nil {
			return nil, false, err
		}
		spec.ScopeParams = params
	}

	_, hasPK := findTag(tableTags, "pk")
	included := hasDynamicDecl || hasPK
	if !included && len(opts.Tables) > 0 {
		for _, want := range opts.Tables {
			if want == table.Rel.Name {
				included = true
				break
			}
		}
	}
	return spec, included, nil
}

func dirName(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

// resolveType 解析列的 dataType 与 Params Go 类型(含枚举识别与 override)。
func resolveType(colWhere string, col *pb.Column, enums map[string][]string, opts *Options, c *ColumnSpec) (dataType string, goType string, err error) {
	// 类型覆盖逃生舱优先。
	if o := matchOverride(opts, col, c); o != nil {
		if o.GoType.Import != "" {
			goType = o.GoType.Package + "." + o.GoType.TypeName
		} else {
			goType = o.GoType.TypeName
		}
	}
	typeName := col.Type.Name
	if vals, isEnum := enums[typeName]; isEnum {
		c.EnumVals = vals
		c.EnumPhysical = typeName
		c.EnumGoType = qualify(enumGoTypeName(typeName), opts)
		if goType == "" {
			goType = c.EnumGoType
		}
		return "enum", goType, nil
	}
	m, err := mapPGType(col)
	if err != nil {
		// 类型不支持动态列:仅当列未声明任何过滤/排序能力时放行(纯投影列)。
		if c.Hidden || c.Sortable || len(c.Ops) > 0 {
			return "", "", fmt.Errorf("%s: %v", colWhere, err)
		}
		return "unmapped", "", nil
	}
	c.NumericCast = m.numericCast
	if goType == "" {
		goType = m.goType
	}
	return m.dataType, goType, nil
}

func matchOverride(opts *Options, col *pb.Column, c *ColumnSpec) *GoTypeOverride {
	for i := range opts.GoTypeOverrides {
		o := &opts.GoTypeOverrides[i]
		if o.Column != "" {
			parts := strings.SplitN(o.Column, ".", 2)
			if len(parts) == 2 && parts[1] == col.Name {
				return o
			}
		} else if o.DbType != "" && o.DbType == col.Type.Name {
			return o
		}
	}
	return nil
}

func qualify(name string, opts *Options) string {
	if opts.ModelImport != "" {
		return opts.ModelPackage + "." + name
	}
	return name
}

// validateTable 表级校验:camel 冲突、@pk、@default_sort、sortable 类型限制。
func validateTable(spec *TableSpec) error {
	// camel 折叠冲突。
	seen := map[string]string{} // key → physical
	for _, c := range spec.Columns {
		if prev, dup := seen[c.Key]; dup {
			return fmt.Errorf("table %s: columns %q and %q fold to the same camelCase key %q",
				spec.Name, prev, c.Physical, c.Key)
		}
		seen[c.Key] = c.Physical
	}

	// @pk 必填:tiebreaker 保证全序,是确定性分页与游标定位的前提。
	if spec.Tiebreaker == "" {
		return fmt.Errorf("table %s: missing @pk directive in table comment (primary key is required for deterministic pagination)", spec.Name)
	}
	pk, ok := seen[spec.Tiebreaker]
	if !ok {
		return fmt.Errorf("table %s: @pk column %q not found", spec.Name, spec.Tiebreaker)
	}
	_ = pk
	for _, c := range spec.Columns {
		if c.Key != spec.Tiebreaker {
			continue
		}
		if c.Hidden {
			return fmt.Errorf("table %s: @pk column %q must not be @hidden (it is force-merged into projection and cursor)", spec.Name, c.Physical)
		}
		if c.Nullable {
			return fmt.Errorf("table %s: @pk column %q must be NOT NULL (seek predicate degenerates only for non-null tiebreaker)", spec.Name, c.Physical)
		}
		if c.DataType == "unmapped" {
			return fmt.Errorf("table %s: @pk column %q has unsupported type for dynamic generation", spec.Name, c.Physical)
		}
	}

	// @default_sort 目标必须存在、已声明 @sortable 且非 @hidden。
	for _, ds := range spec.DefaultSort {
		found := false
		for _, c := range spec.Columns {
			if c.Physical != ds.Column {
				continue
			}
			found = true
			if c.Hidden {
				return fmt.Errorf("table %s: @default_sort target %q is @hidden", spec.Name, ds.Column)
			}
			if !c.Sortable {
				return fmt.Errorf("table %s: @default_sort target %q must declare @sortable", spec.Name, ds.Column)
			}
			if c.DataType == "unmapped" {
				return fmt.Errorf("table %s: @default_sort target %q has unsupported type", spec.Name, ds.Column)
			}
		}
		if !found {
			return fmt.Errorf("table %s: @default_sort column %q not found", spec.Name, ds.Column)
		}
	}

	// @scope 目标列必须存在;不与列能力矩阵耦合——scope 列可以 @hidden、
	// 可以无 @dynamic、可以是任意 PG 类型(类型契约由守卫函数返回形态保证:
	// in 为单列集合且类型一致,eq/比较类为标量且可比较)。
	for _, g := range spec.Scope {
		found := false
		for _, c := range spec.Columns {
			if c.Physical == g.Column {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("table %s: @scope column %q not found", spec.Name, g.Column)
		}
	}

	// sortable 列的类型限制:numeric(pgtype.Numeric 无法编码游标值)与
	// int_array(数组 seek 谓词无法参数化)不可排序。
	for _, c := range spec.Columns {
		if !c.Sortable {
			continue
		}
		if c.NumericCast {
			return fmt.Errorf("column %s.%s: @sortable not supported for numeric columns (use a float8 column or drop @sortable)", spec.Name, c.Physical)
		}
		if c.DataType == "int_array" {
			return fmt.Errorf("column %s.%s: @sortable not supported for int_array columns", spec.Name, c.Physical)
		}
		if c.DataType == "unmapped" {
			return fmt.Errorf("column %s.%s: @sortable requires a mappable type", spec.Name, c.Physical)
		}
	}
	return nil
}

// scopeParamsOf 汇总全部守卫的标识符参数(声明序去重)为 Scope 结构体字段来源:
// 同名参数类型不一致、不同参数名折叠为同一 Go 字段名(如 user_id/user__id)
// 均为生成期 fail——权限载体的字段歧义不允许存在。
func scopeParamsOf(where string, guards []scopeGuard) ([]scopeParam, error) {
	var out []scopeParam
	types := map[string]string{}   // 参数名 → 类型
	goNames := map[string]string{} // Go 字段名 → 参数名
	for _, g := range guards {
		for _, a := range g.Args {
			if a.Literal != "" {
				continue
			}
			if prev, dup := types[a.Name]; dup {
				if prev != a.Type {
					return nil, fmt.Errorf("%s: @scope parameter %q declared with conflicting types %s and %s", where, a.Name, prev, a.Type)
				}
				continue
			}
			types[a.Name] = a.Type
			gc := goName(a.Name)
			if prev, dup := goNames[gc]; dup {
				return nil, fmt.Errorf("%s: @scope parameters %q and %q fold to the same Go field %q", where, prev, a.Name, gc)
			}
			goNames[gc] = a.Name
			out = append(out, scopeParam{Name: a.Name, GoConst: gc, GoType: scopeParamTypes[a.Type]})
		}
	}
	return out, nil
}
