package main

import (
	"fmt"
	"strings"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// catalog.go:GenerateRequest.Catalog → TableSpec(生成期元数据 + 全部生成期校验)。

// ColumnSpec 是一列的生成期规格。
type ColumnSpec struct {
	Physical string
	// SQLName 是 Physical 在 SQL 文本中的安全引用形态(保留字/非常规名加双
	// 引号,见 ident.go):全部发射进 SQL 文本的列名一律取本字段;
	// Physical 保留裸名,用于 DSL 目标匹配与错误文案。
	SQLName    string
	Key        string // camelCase 逻辑键
	GoConst    string // 字段常量结构体成员名(PascalCase)
	ModelField string // sqlc-gen-go 模型结构体字段名(游标取值用)
	Label      string
	DataType   string // dynq.DataType 值;"unmapped" 表示不可过滤/排序仅可投影
	GoType     string // Params Go 类型(已含模型包限定前缀)
	// IsArray 是 catalog 的数组形态标记。validateTable 层不可见 catalog 的
	// IsArray,能力面校验(如枚举数组拒绝)依赖该记录。
	IsArray bool
	// DefaultOpsGranted 报告 Ops 是否来自 default_ops 策略授予(非显式
	// @dynamic):错误文案按触发器分支时使用。
	DefaultOpsGranted bool
	Nullable          bool
	Ops               []string
	Sortable          bool
	Selectable        bool
	Hidden            bool
	// UpdatableMode 是列级 @updatable 的声明形态:
	// ""(未声明) / "auto" / "set" / "where" / "false" / "list"(显式清单)。
	UpdatableMode string
	// SetCapable / WhereCapable 是 @updatable 的两个能力面:SET 白名单与
	// WHERE 定位方法族。两者独立,可只开一侧(auto 除外)。
	SetCapable   bool
	WhereCapable bool
	// UpdateOps 是 Where 方法族的能力清单:auto / where 取类型矩阵,
	// list 取显式清单(预设先与类型矩阵取交集),set / false 为空。
	// 与 @dynamic 的 Ops(读面)完全独立。
	UpdateOps  []string
	EnumVals   []string
	EnumGoType string // 同包模型枚举类型名(如 UserStatus);跨包时已含前缀
	// EnumPhysical 是 PG 枚举类型名(如 user_status),用于占位符显式 cast。
	EnumPhysical string
	// OverrideImport 是命中的 go_type_overrides 声明(nil = 未命中或无 import),
	// 供 importsSection 收集产物 import(空路径声明会被静默跳过,产物不可编译)。
	OverrideImport *GoTypeOverride
	// OverrideDivergent 报告 override 类型与默认映射(即协议门收敛)类型不一致:
	// 读面值 op 的 Params 槽位与时间型 between 的边界比较会生成与 override
	// 类型不匹配的代码,由 validateTable 生成期拒绝。
	OverrideDivergent bool
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
	Scope           []scopeGuard      // @scope 守卫声明;空 = 未声明
	ScopeParams     []scopeParam      // 守卫标识符参数的并集(声明序),驱动 Scope 结构体
	Filters         []filterDirective // @filter 服务端固定条件;空 = 未声明
	// UpdateEnabled 报告本表是否生成动态更新面:至少一列 set-capable 且未被
	// 表级 @update: false 关闭。
	UpdateEnabled bool
	// ReadEnabled 报告本表是否生成读面(= 声明了 @pk)。无 @pk 表只生成
	// 更新面(header + marker + Set / Where + UpdateXxx)。
	ReadEnabled bool
	// ReadDeclared 报告是否出现读侧指令(无 @pk 表据此生成期 fail)。
	ReadDeclared bool
	// Updatables 是 set-capable 列(声明序),驱动 Set 方法族;
	// WHERE 方法族另按 WhereCapable / @pk 自动定位生成。
	Updatables []*ColumnSpec
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
// 第二返回值是 deprecation 提示(旧指令名使用,按列去重),由入口输出到 stderr。
func buildTableSpecs(req *pb.GenerateRequest, opts *Options) ([]*TableSpec, []string, error) {
	schema := findSchema(req.Catalog, opts.Schema)
	if schema == nil {
		return nil, nil, fmt.Errorf("schema %q not found in catalog", opts.Schema)
	}
	// 枚举名 → 值列表。
	enums := enumMap(schema)

	// 逐表解析注释 DSL。
	byName := map[string]*TableSpec{}
	var order []*TableSpec
	var deprecations []string
	for _, table := range schema.Tables {
		spec, included, deps, err := buildTable(table, schema.Name, opts, enums)
		if err != nil {
			return nil, nil, err
		}
		deprecations = append(deprecations, deps...)
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
				return nil, nil, fmt.Errorf("table %q listed in options not found in schema %q (or it has no @dynamic / @pk / set-capable @updatable declarations)", want, opts.Schema)
			}
			picked = append(picked, spec)
		}
		order = picked
	}

	for _, spec := range order {
		if err := validateTable(spec); err != nil {
			return nil, nil, err
		}
		// default_ops 的读策略只作用于含 @pk 的读面:无 @pk 表不生成读面,
		// 策略授予的过滤能力会被静默丢弃(死配置)。与"杜绝静默忽略"一致,
		// 显式 fail;出路是补 @pk、或 default_ops: none 后按列显式 @dynamic。
		if !spec.ReadEnabled && opts.DefaultOps != "none" {
			return nil, nil, fmt.Errorf("table %s: default_ops %q would grant read-side ops but the table has no @pk (the default ops policy only shapes @pk read surfaces); declare @pk or set default_ops: none and opt columns in via explicit @dynamic", spec.Name, opts.DefaultOps)
		}
	}
	// 更新面生成三个包级符号(<单数>Table / <单数>Update / <单数>UpdateOption):
	// 与 sqlc-gen-go 同包时不得与任何表的模型类型撞名(如 user_table 的模型
	// UserTable 会撞 users 的 marker)。撞名将导致生成物不可编译,生成期即 fail。
	modelNames := make(map[string]bool, len(schema.Tables))
	for _, t := range schema.Tables {
		modelNames[modelName(t.Rel.Name)] = true
	}
	for _, spec := range order {
		if !spec.UpdateEnabled {
			continue
		}
		for _, sym := range []string{spec.Singular + "Table", spec.Singular + "Update", spec.Singular + "UpdateOption"} {
			if modelNames[sym] {
				return nil, nil, fmt.Errorf("table %s: generated update symbol %q collides with an sqlc model type of the same name (rename the colliding table)", spec.Name, sym)
			}
		}
	}
	if len(order) == 0 {
		return nil, nil, fmt.Errorf("no tables selected for dynamic generation in schema %q (opt a table in via table comment [@pk: <col>], any column [@dynamic: ...], any column [@updatable] with set capability, or options.tables)", opts.Schema)
	}
	return order, deprecations, nil
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

// buildTable 解析单表;included=false 表示该表无读/写面声明且未被点名。
// 第三返回值是本表的 deprecation 提示(旧指令名使用)。
func buildTable(table *pb.Table, schemaName string, opts *Options, enums map[string][]string) (*TableSpec, bool, []string, error) {
	var deprecations []string
	tableWhere := fmt.Sprintf("table %s", table.Rel.Name)
	_, tableTags, err := parseComment(tableWhere, table.Comment)
	if err != nil {
		return nil, false, nil, err
	}
	tableTags, tableDeps := normalizeTagNames(tableTags)
	tableTags = mergeTags(tableTags)
	deprecations = append(deprecations, tableDeps...)
	if err := validateTagNames(tableWhere, "table", tableTags); err != nil {
		return nil, false, nil, err
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
			return nil, false, nil, err
		}
		tags, colDeps := normalizeTagNames(tags)
		for _, d := range colDeps {
			deprecations = append(deprecations, colWhere+": "+d)
		}
		tags = mergeTags(tags)
		if err := validateTagNames(colWhere, "column", tags); err != nil {
			return nil, false, nil, err
		}

		c := ColumnSpec{
			Physical:   col.Name,
			SQLName:    sqlIdent(col.Name),
			Key:        camelKey(col.Name),
			GoConst:    goName(col.Name),
			ModelField: goName(col.Name),
			Label:      label,
			IsArray:    col.IsArray,
			Nullable:   !col.NotNull,
			GoType:     "unmapped",
			DataType:   "unmapped",
		}
		if c.Label == "" {
			c.Label = col.Name
		}

		dynTag, hasDyn := findTag(tags, "dynamic")
		_, sortableTag := findTag(tags, "sortable")
		_, internalTag := findTag(tags, "internal")
		_, hideableTag := findTag(tags, "hideable")
		updatableTag, hasUpdatable := findTag(tags, "updatable")
		_, hasAPI := findTag(tags, "api")

		// @internal 与任一正向标记组合 → 生成期 fail。@updatable 是写能力,
		// 不属协议可见性域:@internal + @updatable 合法(服务端可写、协议不可见)。
		if internalTag && (hasDyn || sortableTag || hideableTag || hasAPI) {
			return nil, false, nil, fmt.Errorf("%s: @internal cannot be combined with @dynamic/@sortable/@hideable/@api", colWhere)
		}
		c.Hidden = internalTag
		c.Sortable = sortableTag
		c.Selectable = hideableTag
		if hasUpdatable {
			mode, err := parseUpdatableMode(colWhere, updatableTag.Payload)
			if err != nil {
				return nil, false, nil, err
			}
			c.UpdatableMode = mode
		}

		dataType, goType, err := resolveType(colWhere, table.Rel.Name, col, enums, opts, &c)
		if err != nil {
			return nil, false, nil, err
		}
		c.DataType = dataType
		c.GoType = goType

		// @dynamic:显式声明或 default_ops 策略(读面;与 Update 完全独立)。
		// 枚举数组的能力面拒绝在 validateTable(G2:检查只作用于入选表)。
		if hasDyn {
			hasDynamicDecl = true
			ops, err := expandDynamicItems(colWhere, dataType, dynTag.Payload, c.Nullable)
			if err != nil {
				return nil, false, nil, err
			}
			c.Ops = ops
			c.Directives = append(c.Directives, fmt.Sprintf("@dynamic: %s", strings.Join(ops, ", ")))
		} else if !c.Hidden && opts.DefaultOps != "none" && dataType != "unmapped" {
			ops, err := expandDynamicItems(colWhere, dataType, opts.DefaultOps, c.Nullable)
			if err != nil {
				return nil, false, nil, err
			}
			c.Ops = ops
			c.DefaultOpsGranted = true
		}
		for _, name := range []string{"sortable", "internal", "hideable"} {
			if t, ok := findTag(tags, name); ok {
				if t.Payload != "" {
					return nil, false, nil, fmt.Errorf("%s: @%s takes no payload, got %q", colWhere, name, t.Payload)
				}
				c.Directives = append(c.Directives, "@"+name)
			}
		}
		// @updatable:写面能力(依赖 dataType,在类型解析之后展开)。
		if hasUpdatable {
			if err := applyUpdatable(colWhere, &c, updatableTag.Payload); err != nil {
				return nil, false, nil, err
			}
			c.Directives = append(c.Directives, updatableEcho(&c))
		}
		// @api:选项数据源声明;仅 integer/uuid/string 引用字段支持。
		if t, ok := findTag(tags, "api"); ok {
			api, err := parseAPIDirective(colWhere, t)
			if err != nil {
				return nil, false, nil, err
			}
			switch dataType {
			case "integer", "uuid", "string":
			default:
				return nil, false, nil, fmt.Errorf("%s: @api not supported for dataType %s (want integer|uuid|string reference fields)", colWhere, dataType)
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
	// SET 白名单按声明序汇总(指针指向 spec.Columns 元素,后续不再扩容)。
	for i := range spec.Columns {
		if spec.Columns[i].SetCapable {
			spec.Updatables = append(spec.Updatables, &spec.Columns[i])
		}
	}

	// 表级指令。
	if t, ok := findTag(tableTags, "page"); ok {
		def, max, err := parsePageDirective(tableWhere, t)
		if err != nil {
			return nil, false, nil, err
		}
		spec.PageDefault, spec.PageMax = def, max
		spec.TableDirectives = append(spec.TableDirectives, fmt.Sprintf("@page: %d/%d", def, max))
	}
	if t, ok := findTag(tableTags, "default_sort"); ok {
		column, desc, err := parseDefaultSortDirective(tableWhere, t)
		if err != nil {
			return nil, false, nil, err
		}
		spec.DefaultSort = append(spec.DefaultSort, sortDirective{Column: column, Desc: desc})
		spec.TableDirectives = append(spec.TableDirectives, fmt.Sprintf("@default_sort: %s %s", column, dirName(desc)))
	}
	// @pk:读面入选条件与无条件 tiebreaker。有 @pk 时 Update 的 Where 方法族
	// 自动包含该列的完整类型矩阵(定位方法无需额外声明)。
	if t, ok := findTag(tableTags, "pk"); ok {
		if t.Payload == "" || strings.ContainsAny(t.Payload, " /") {
			return nil, false, nil, fmt.Errorf("%s: @pk wants a single column name, got %q", tableWhere, t.Payload)
		}
		spec.Tiebreaker = camelKey(t.Payload)
		spec.ReadEnabled = true
		spec.TableDirectives = append(spec.TableDirectives, "@pk: "+t.Payload)
	}
	// @update:表级更新面开关。缺省 auto;裸 @update = true;true / auto 开启;
	// false 关闭;其他 payload → fail。策略头回显声明原文。
	updateDeclared, updateEnabled := false, true
	if t, ok := findTag(tableTags, "update"); ok {
		enabled, echo, err := parseUpdateDirective(tableWhere, t)
		if err != nil {
			return nil, false, nil, err
		}
		updateDeclared, updateEnabled = true, enabled
		spec.TableDirectives = append(spec.TableDirectives, echo)
	}
	if !updateEnabled && hasEffectiveUpdatable(spec.Columns, spec.Tiebreaker) {
		return nil, false, nil, fmt.Errorf("%s: @update: false conflicts with @updatable columns; drop one side", tableWhere)
	}
	if updateDeclared && updateEnabled && len(spec.Updatables) == 0 {
		return nil, false, nil, fmt.Errorf("%s: @update declares an update surface but no column is set-capable (dead configuration)", tableWhere)
	}
	spec.UpdateEnabled = updateEnabled && len(spec.Updatables) > 0
	// 能力覆盖:@pk 自动提供定位方法;无 @pk 时 set-capable 必须有显式
	// where-capable 列,否则更新面不可用(每次都会撞零 where 守卫)。
	hasWhereDecl := false
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if c.Key != spec.Tiebreaker && c.WhereCapable {
			hasWhereDecl = true
			break
		}
	}
	if len(spec.Updatables) > 0 && !hasWhereDecl && !spec.ReadEnabled {
		return nil, false, nil, fmt.Errorf("%s: set-capable @updatable requires at least one where-capable @updatable column (no @pk locator in this table)", tableWhere)
	}
	if len(spec.Updatables) == 0 && hasWhereDecl {
		return nil, false, nil, fmt.Errorf("%s: where-capable @updatable requires at least one set-capable @updatable column", tableWhere)
	}
	if t, ok := findTag(tableTags, "scope"); ok {
		guards, err := parseScopeDirective(tableWhere, t)
		if err != nil {
			return nil, false, nil, err
		}
		spec.Scope = guards
		spec.TableDirectives = append(spec.TableDirectives, "@scope: "+scopeEcho(guards))
		params, err := scopeParamsOf(tableWhere, guards)
		if err != nil {
			return nil, false, nil, err
		}
		spec.ScopeParams = params
	}
	if t, ok := findTag(tableTags, "filter"); ok {
		filters, err := parseFilterDirective(tableWhere, t)
		if err != nil {
			return nil, false, nil, err
		}
		spec.Filters = filters
		spec.TableDirectives = append(spec.TableDirectives, "@filter: "+filterEcho(filters))
	}

	// 读侧指令登记:无 @pk 表只允许"无读面更新面";此处登记供 validateTable
	// 判定(含 @pk 表不受影响)。
	spec.ReadDeclared = hasDynamicDecl || len(spec.DefaultSort) > 0 || len(spec.Scope) > 0
	if _, ok := findTag(tableTags, "page"); ok {
		spec.ReadDeclared = true
	}
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if c.Sortable || c.Selectable || c.API != nil {
			spec.ReadDeclared = true
			break
		}
	}

	included := hasDynamicDecl || spec.ReadEnabled || len(spec.Updatables) > 0
	if !included && len(opts.Tables) > 0 {
		for _, want := range opts.Tables {
			if want == table.Rel.Name {
				included = true
				break
			}
		}
	}
	return spec, included, deprecations, nil
}

func dirName(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

// applyUpdatable 在类型解析之后把 @updatable 形态落成两个能力面与
// WHERE 方法清单(auto / where 取类型矩阵,list 取显式清单)。
func applyUpdatable(where string, c *ColumnSpec, payload string) error {
	switch c.UpdatableMode {
	case "false":
		return nil // 惰性声明:不参与任何能力与判定
	case "set":
		c.SetCapable = true
	case "where":
		c.WhereCapable = true
		c.UpdateOps = matrixOps(c.DataType)
	case "auto":
		c.SetCapable, c.WhereCapable = true, true
		c.UpdateOps = matrixOps(c.DataType)
	default: // list
		ops, err := expandUpdatableItems(where, c.DataType, payload, c.Nullable)
		if err != nil {
			return err
		}
		c.SetCapable, c.WhereCapable = true, true
		c.UpdateOps = ops
	}
	return nil
}

// hasEffectiveUpdatable 报告是否存在非 false 形态的 @updatable 声明
// (@update: false 的死声明判定用)。pk 上的 where 形态是惰性声明
// (生成物只由 @pk 自动矩阵决定),不构成死声明。
func hasEffectiveUpdatable(cols []ColumnSpec, pk string) bool {
	for i := range cols {
		mode := cols[i].UpdatableMode
		if mode == "" || mode == "false" {
			continue
		}
		if pk != "" && cols[i].Key == pk && mode == "where" {
			continue
		}
		return true
	}
	return false
}

// errMissingPK 是读面必备主键的既有文案(无 @pk 且出现读侧指令、或既无
// 读面也无更新面时复用,保持存量报错稳定)。
func errMissingPK(table string) error {
	return fmt.Errorf("table %s: missing @pk directive in table comment (primary key is required for deterministic pagination)", table)
}

// resolveType 解析列的 dataType 与 Params Go 类型(含枚举识别与 override)。
func resolveType(colWhere, tableName string, col *pb.Column, enums map[string][]string, opts *Options, c *ColumnSpec) (dataType string, goType string, err error) {
	// 类型覆盖逃生舱优先。defaultGoType 记录未覆盖时的映射类型,供分叉检测:
	// 协议门按 dataType 收敛(AsInt64/AsUUID/AsJSON...),override 类型不一致
	// 时读面值 op 槽位的生成物不可编译。
	var defaultGoType string
	overrideHit := false
	if o := matchOverride(opts, tableName, col, c); o != nil {
		overrideHit = true
		if o.GoType.Import != "" {
			// import 别名缺省取路径末段(与 sqlc-gen-go 同一约定)。
			if o.GoType.Package == "" {
				o.GoType.Package = o.GoType.Import[strings.LastIndexByte(o.GoType.Import, '/')+1:]
			}
			goType = o.GoType.Package + "." + o.GoType.TypeName
			c.OverrideImport = o
		} else {
			goType = o.GoType.TypeName
		}
	}
	typeName := col.Type.Name
	if vals, isEnum := enums[typeName]; isEnum {
		// 枚举数组在 catalog 中的 Type.Name 仍是枚举名:非 false 的 @updatable
		// 会生成标量枚举的 Set/Where(类型不匹配,运行期必失败),生成期先拒绝。
		// 该限制只针对写面,不扩展读侧(@dynamic / @sortable)行为。
		if col.IsArray && c.UpdatableMode != "" && c.UpdatableMode != "false" {
			return "", "", fmt.Errorf("%s: array type %q is not supported for @updatable", colWhere, typeName)
		}
		c.EnumVals = vals
		c.EnumPhysical = typeName
		c.EnumGoType = qualify(enumGoTypeName(typeName), opts)
		defaultGoType = c.EnumGoType
		if goType == "" {
			goType = c.EnumGoType
		}
		if overrideHit {
			c.OverrideDivergent = goType != defaultGoType
		}
		return "enum", goType, nil
	}
	m, err := mapPGType(col)
	if err != nil {
		// 类型不支持动态列:仅当列未声明任何过滤/排序/写入能力时放行(纯投影列)。
		// @updatable: false 是惰性声明,不要求类型可映射。
		if c.Hidden || c.Sortable || (c.UpdatableMode != "" && c.UpdatableMode != "false") || len(c.Ops) > 0 {
			return "", "", fmt.Errorf("%s: %v", colWhere, err)
		}
		return "unmapped", "", nil
	}
	defaultGoType = m.goType
	if goType == "" {
		goType = m.goType
	}
	if overrideHit {
		c.OverrideDivergent = goType != defaultGoType
	}
	return m.dataType, goType, nil
}

func matchOverride(opts *Options, tableName string, col *pb.Column, c *ColumnSpec) *GoTypeOverride {
	for i := range opts.GoTypeOverrides {
		o := &opts.GoTypeOverrides[i]
		if o.Column != "" {
			// column 形态为 table.column:表名与列名都须精确一致——仅按列名
			// 匹配会让 override 泄漏到其他表的同名列(类型错配)。
			parts := strings.SplitN(o.Column, ".", 2)
			if len(parts) == 2 && parts[0] == tableName && parts[1] == col.Name {
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

	// @pk 必填:读面(tiebreaker 保证全序)必须;无 @pk 表只在"无读面更新面"
	// 分支下合法(读侧指令或零更新能力 → 仍按原文案 fail)。
	if spec.Tiebreaker == "" {
		if spec.ReadDeclared || !spec.UpdateEnabled {
			return errMissingPK(spec.Name)
		}
		// 更新面 between 的分叉检测与常规表共用:边界比较的发射不依赖读面,
		// 无 @pk 纯更新面表正是 between 的主要使用形态(G1)。
		if err := updateBetweenDivergenceError(spec); err != nil {
			return err
		}
		// @filter 是写面合法指令,目标列存在性仍需校验。
		return validateFilterTargets(spec)
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
			return fmt.Errorf("table %s: @pk column %q must not be @internal (it is force-merged into projection and cursor)", spec.Name, c.Physical)
		}
		if c.Nullable {
			return fmt.Errorf("table %s: @pk column %q must be NOT NULL (seek predicate degenerates only for non-null tiebreaker)", spec.Name, c.Physical)
		}
		if c.DataType == "unmapped" {
			return fmt.Errorf("table %s: @pk column %q has unsupported type for dynamic generation", spec.Name, c.Physical)
		}
		if c.SetCapable {
			return fmt.Errorf("table %s: @pk column %q must not be set-capable @updatable (primary key is immutable)", spec.Name, c.Physical)
		}
		// tiebreaker 游标可编码白名单:tiebreaker 值嵌入分页游标,第二页起
		// EncodeCursor 才取值,不可编码类型到运行期才暴露失败,生成期先行
		// fail(unmapped 已由上方分支先行报错,此处为其余类型的统一兜底)。
		switch c.DataType {
		case "integer", "number", "decimal", "datetime", "date", "uuid", "boolean", "string", "enum":
		default:
			return fmt.Errorf("table %s: @pk column %q has unsupported type %q for cursor encoding (tiebreaker values are embedded in the pagination cursor)", spec.Name, c.Physical, c.DataType)
		}
	}

	// @default_sort 目标必须存在、已声明 @sortable 且非 @internal。
	for _, ds := range spec.DefaultSort {
		found := false
		for _, c := range spec.Columns {
			if c.Physical != ds.Column {
				continue
			}
			found = true
			if c.Hidden {
				return fmt.Errorf("table %s: @default_sort target %q is @internal", spec.Name, ds.Column)
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

	// @scope 目标列必须存在;不与列能力矩阵耦合——scope 列可以 @internal、
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

	// @filter 目标列必须存在;与能力矩阵解耦——filter 列可以 @internal、可以
	// 无 @dynamic、可以是任意 PG 类型(@scope 同款先验:类型契约由字面量与列
	// 类型的 PG 兼容性保证,生成期不做类型推导)。@internal 列由生成的 registry
	// 字段 Hidden 标记对协议不可见,纯服务端不变量列建议保持 @internal。
	if err := validateFilterTargets(spec); err != nil {
		return err
	}

	// 枚举数组:读能力面不支持——按标量枚举展开会生成 enum[] 与 enum 的非法
	// 比较 SQL,游标取值同样无法编译;仅纯投影放行。写面已在 resolveType 的
	// 枚举分支拒绝非 false 的 @updatable。检查置于入选表集:未入选表的列
	// 声明与 default_ops 策略本不生效,不因此 fail(G2)。
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if c.DataType != "enum" || !c.IsArray {
			continue
		}
		if c.Sortable {
			return fmt.Errorf("column %s.%s: array type %q is not supported for @sortable (enum arrays are projection-only)", spec.Name, c.Physical, c.EnumPhysical)
		}
		if len(c.Ops) > 0 {
			if c.DefaultOpsGranted {
				return fmt.Errorf("column %s.%s: array type %q is not supported for dynamic filtering granted by default_ops; mark the column @internal, or set default_ops: none", spec.Name, c.Physical, c.EnumPhysical)
			}
			return fmt.Errorf("column %s.%s: array type %q is not supported for @dynamic (enum arrays are projection-only)", spec.Name, c.Physical, c.EnumPhysical)
		}
	}

	// go_type_overrides 与协议门收敛类型的分叉检测:读面值 op 的 Params 槽位
	// 按 dataType 收敛(AsInt64/AsUUID/AsJSON...),字段类型却是 override 类型,
	// 分叉即不可编译。模糊三 op(恒 string)、is_null/not_null(恒 bool)与
	// 更新面 Set/Where 的值透传(any)不引用收敛类型,分叉列仍可经这些面使用。
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if !c.OverrideDivergent {
			continue
		}
		for _, op := range c.Ops {
			if goTypeOpRead(op) {
				return fmt.Errorf("column %s.%s: go_type_overrides type %q diverges from the %s coercion type; read-side op %q would generate uncompilable Params (drop the override, or keep only fuzzy / is_null / not_null ops)",
					spec.Name, c.Physical, c.GoType, c.DataType, op)
			}
		}
	}
	if err := updateBetweenDivergenceError(spec); err != nil {
		return err
	}

	// sortable 列的类型限制:int_array(数组 seek 谓词无法参数化)与 unmapped
	// 不可排序。numeric 可排序:游标值编码为十进制字符串,seek 占位符发射
	// ?::numeric,比较在服务端以 numeric 精确进行。float4/smallint 放行:
	// 其模型字段(float32/int16)由 dynq 游标编码分支覆盖,seek 参数按
	// number/integer 收敛为 float64/int64,PG 跨类型比较不损索引。
	for _, c := range spec.Columns {
		if !c.Sortable {
			continue
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

// validateFilterTargets 校验 @filter 目标列存在(读面表与无 @pk 更新面表共用)。
func validateFilterTargets(spec *TableSpec) error {
	for _, f := range spec.Filters {
		found := false
		for _, c := range spec.Columns {
			if c.Physical == f.Column {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("table %s: @filter column %q not found", spec.Name, f.Column)
		}
	}
	return nil
}

// updateBetweenDivergenceError 拒绝分叉 override 列的更新面 between:边界比较
// 按 dataType 发射(数值为 lo > hi,时间型为 lo.After(hi) 方法调用),override
// 类型不保证可比较/携带 After 方法,一律不可编译;pk 自动定位矩阵同样发射。
// 无 @pk 纯更新面表不经 validateTable 主流程(提前返回),由该分支单独调用
// 本检测(G1:分叉故障类不因表形态而豁免)。
func updateBetweenDivergenceError(spec *TableSpec) error {
	if !spec.UpdateEnabled {
		return nil
	}
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if !c.OverrideDivergent {
			continue
		}
		ops := c.UpdateOps
		if spec.Tiebreaker != "" && c.Key == spec.Tiebreaker {
			ops = matrixOps(c.DataType) // pk 自动定位矩阵按完整类型矩阵发射
		}
		for _, op := range ops {
			if op != "between" {
				continue
			}
			cmp := "lo > hi"
			if c.DataType == "datetime" || c.DataType == "date" {
				cmp = "lo.After(hi)"
			}
			return fmt.Errorf("column %s.%s: go_type_overrides type %q diverges from the %s coercion type; @updatable between emits %s, which does not compile for the override type (drop the override or the between op)",
				spec.Name, c.Physical, c.GoType, c.DataType, cmp)
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
