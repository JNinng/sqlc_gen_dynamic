package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	// SortToken 报告 @dynamic 显式声明了 sort 能力位(仅用于文件头回显)。
	SortToken bool
	Hidden    bool
	// Visibility 是前端列表可见性(always / optional / hidden);空 = optional。
	// 来自 options.ui,只影响前端初始显隐,不参与协议校验与默认投影。
	Visibility string
	// UpdatableMode 是列级 @updatable 的声明形态:
	// ""(未声明) / "auto" / "set" / "locate" / "list"(显式清单)。
	UpdatableMode string
	// SetCapable / WhereCapable 是 @updatable 的两个能力面:SET 白名单与
	// WHERE 定位方法族。两者独立,可只开一侧(auto 除外)。
	SetCapable   bool
	WhereCapable bool
	// UpdateOps 是 Where 方法族的能力清单:auto / locate 取类型矩阵,
	// list 取显式清单(预设先与类型矩阵取交集),set 为空。
	// 与 @dynamic 的 Ops(读面)完全独立。
	UpdateOps  []string
	EnumVals   []string
	EnumGoType string // 同包模型枚举类型名(如 UserStatus);跨包时已含前缀
	// EnumPhysical 是 PG 枚举类型名(如 user_status),用于占位符显式 cast。
	EnumPhysical string
	// OverrideImport 是命中的 go_type_overrides 声明(nil = 未命中或无 import;
	// 无 import 的 type_name 形态不记录,importsSection 只发射带路径的 import,
	// 避免空路径 import 进产物),供 importsSection 收集产物 import。
	OverrideImport *GoTypeOverride
	// OverrideDivergent 报告 override 类型与默认映射(即协议门收敛)类型不一致:
	// 读面值 op 的 Params 槽位与时间型 between 的边界比较会生成与 override
	// 类型不匹配的代码,由 validateTable 生成期拒绝。
	OverrideDivergent bool
	// API 是远程选项数据源声明(options.ui, nil = 无)。
	API *APISpec
	// Desc 是字段描述(options.ui;空 = 未声明),仅进入前端 schema。
	Desc string
	// Order 是 schema 排序序号(options.ui;nil = 未标注)。
	Order *int
	// Options 是静态字典选项(options.ui;nil = 未声明)。
	Options []OptionSpec
	// DSL 回显(文件头策略来源):只列 DDL 能力指令。
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
	// UpdateEnabled 报告本表是否生成动态更新面:至少一列 set-capable。
	UpdateEnabled bool
	// ReadEnabled 报告本表是否生成读面(= 声明了 @pk)。无 @pk 表只生成
	// 更新面(header + marker + Set / Where + UpdateXxx / UpdateXxxReturning)。
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
func buildTableSpecs(req *pb.GenerateRequest, opts *Options) ([]*TableSpec, error) {
	schema := findSchema(req.Catalog, opts.Schema)
	if schema == nil {
		return nil, fmt.Errorf("schema %q not found in catalog", opts.Schema)
	}
	// 枚举名 → 值列表。
	enums := enumMap(schema)

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
				return nil, fmt.Errorf("table %q listed in options not found in schema %q (or it has no @dynamic / @pk / set-capable @updatable declarations)", want, opts.Schema)
			}
			picked = append(picked, spec)
		}
		order = picked
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("no tables selected for dynamic generation in schema %q (opt a table in via table comment [@pk: <col>], any column [@dynamic: ...], any column [@updatable] with set capability, or options.tables)", opts.Schema)
	}

	// 分页策略与 UI 元数据只作用于入选表;manifest 命中未入选表 → 死配置 fail。
	if err := applyPagePolicy(schema, order, opts); err != nil {
		return nil, err
	}
	if err := applyUIManifest(schema, order, opts); err != nil {
		return nil, err
	}

	for _, spec := range order {
		if err := validateTable(spec); err != nil {
			return nil, err
		}
		// default_ops 的读策略只作用于含 @pk 的读面:无 @pk 表不生成读面,
		// 策略授予的过滤能力会被静默丢弃(死配置)。与"杜绝静默忽略"一致,
		// 显式 fail;出路是补 @pk、或 default_ops: none 后按列显式 @dynamic。
		if !spec.ReadEnabled && opts.DefaultOps != "none" {
			return nil, fmt.Errorf("table %s: default_ops %q would grant read-side ops but the table has no @pk (the default ops policy only shapes @pk read surfaces); declare @pk or set default_ops: none and opt columns in via explicit @dynamic", spec.Name, opts.DefaultOps)
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
				return nil, fmt.Errorf("table %s: generated update symbol %q collides with an sqlc model type of the same name (rename the colliding table)", spec.Name, sym)
			}
		}
	}
	return order, nil
}

// applyPagePolicy 把 options.page 落到入选表:全局值打底、表级覆盖优先。
// tables 中的表必须存在于 schema 且被生成范围选中,否则死配置 fail。
func applyPagePolicy(schema *pb.Schema, selected []*TableSpec, opts *Options) error {
	all := make(map[string]bool, len(schema.Tables))
	for _, t := range schema.Tables {
		all[t.Rel.Name] = true
	}
	sel := make(map[string]*TableSpec, len(selected))
	for _, spec := range selected {
		sel[spec.Name] = spec
		spec.PageDefault, spec.PageMax = opts.globalPageDefault, opts.globalPageMax
	}
	for _, name := range sortedKeys(opts.Page.Tables) {
		if !all[name] {
			return fmt.Errorf("page.tables.%s: table not found in schema %q", name, opts.Schema)
		}
		spec, ok := sel[name]
		if !ok {
			return fmt.Errorf("page.tables.%s: table is not selected for generation (dead configuration)", name)
		}
		def, max, _, err := opts.Page.forTable(name, opts.globalPageDefault, opts.globalPageMax)
		if err != nil {
			return err
		}
		spec.PageDefault, spec.PageMax = def, max
	}
	return nil
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
func buildTable(table *pb.Table, schemaName string, opts *Options, enums map[string][]string) (*TableSpec, bool, error) {
	tableWhere := fmt.Sprintf("table %s", table.Rel.Name)
	tableTags, err := parseComment(tableWhere, table.Comment)
	if err != nil {
		return nil, false, err
	}
	tableTags = mergeTags(tableTags)
	if err := validateTagNames(tableWhere, "table", tableTags); err != nil {
		return nil, false, err
	}

	// @pk 提前提取:列级解析需要知道主键(pk 不可写),pk 列同时隐式可排序。
	pkColumn := ""
	if t, ok := findTag(tableTags, "pk"); ok {
		if t.Payload == "" || strings.ContainsAny(t.Payload, " /") {
			return nil, false, fmt.Errorf("%s: @pk wants a single column name, got %q", tableWhere, t.Payload)
		}
		pkColumn = t.Payload
	}

	spec := &TableSpec{
		Name:      table.Rel.Name,
		Resource:  table.Rel.Name,
		ModelName: modelName(table.Rel.Name),
		Singular:  modelName(table.Rel.Name),
	}

	hasDynamicDecl := false
	for _, col := range table.Columns {
		colWhere := fmt.Sprintf("column %s.%s", table.Rel.Name, col.Name)
		tags, err := parseComment(colWhere, col.Comment)
		if err != nil {
			return nil, false, err
		}
		tags = mergeTags(tags)
		if err := validateTagNames(colWhere, "column", tags); err != nil {
			return nil, false, err
		}

		c := ColumnSpec{
			Physical:   col.Name,
			SQLName:    sqlIdent(col.Name),
			Key:        camelKey(col.Name),
			GoConst:    goName(col.Name),
			ModelField: goName(col.Name),
			Label:      col.Name, // 人类注释文本不参与生成;label 缺省回落物理列名
			IsArray:    col.IsArray,
			Nullable:   !col.NotNull,
			GoType:     "unmapped",
			DataType:   "unmapped",
		}

		dynTag, hasDyn := findTag(tags, "dynamic")
		internalTag, hasInternal := findTag(tags, "internal")
		updatableTag, hasUpdatable := findTag(tags, "updatable")

		// @internal 与读取能力声明组合 → 生成期 fail。@updatable 是写能力,
		// 不属协议可见性域:@internal + @updatable 合法(服务端可写、协议不可见)。
		if hasInternal && hasDyn {
			return nil, false, fmt.Errorf("%s: @internal cannot be combined with @dynamic", colWhere)
		}
		c.Hidden = hasInternal
		if hasUpdatable {
			// 主键不可改写:pk 上的任何 @updatable 形态(含 locate)一律拒绝。
			if pkColumn != "" && col.Name == pkColumn {
				return nil, false, fmt.Errorf("%s: @updatable is not allowed on the @pk column %q (primary key is immutable)", colWhere, col.Name)
			}
			mode, err := parseUpdatableMode(colWhere, updatableTag.Payload)
			if err != nil {
				return nil, false, err
			}
			c.UpdatableMode = mode
		}

		dataType, goType, err := resolveType(colWhere, table.Rel.Name, col, enums, opts, &c)
		if err != nil {
			return nil, false, err
		}
		c.DataType = dataType
		c.GoType = goType

		// @dynamic:显式声明或 default_ops 策略(读面;与 Update 完全独立)。
		// sort 是能力位(只置 Sortable),不生成 Params 字段。
		// 枚举数组的能力面拒绝在 validateTable(检查只作用于入选表)。
		if hasDyn {
			hasDynamicDecl = true
			ops, sortTok, err := expandDynamicItems(colWhere, dataType, dynTag.Payload, c.Nullable)
			if err != nil {
				return nil, false, err
			}
			c.Ops = ops
			if sortTok {
				c.Sortable, c.SortToken = true, true
			}
			// 只声明 sort 时("去掉 sort 后 op 为空")仍应用 default_ops,
			// 不得因 sort 声明丢失过滤能力。有显式 op 项但展开为空 → 已由
			// expandDynamicItems fail。
			if !dynamicPayloadHasOps(dynTag.Payload) && len(ops) == 0 &&
				!c.Hidden && opts.DefaultOps != "none" && dataType != "unmapped" {
				ops, _, err = expandDynamicItems(colWhere, dataType, opts.DefaultOps, c.Nullable)
				if err != nil {
					return nil, false, err
				}
				c.Ops = ops
				c.DefaultOpsGranted = true
			}
			if echo := dynamicEcho(c.SortToken, ops, c.DefaultOpsGranted); echo != "" {
				c.Directives = append(c.Directives, echo)
			}
		} else if !c.Hidden && opts.DefaultOps != "none" && dataType != "unmapped" {
			ops, _, err := expandDynamicItems(colWhere, dataType, opts.DefaultOps, c.Nullable)
			if err != nil {
				return nil, false, err
			}
			c.Ops = ops
			c.DefaultOpsGranted = true
		}
		if hasInternal {
			if internalTag.Payload != "" {
				return nil, false, fmt.Errorf("%s: @internal takes no payload, got %q", colWhere, internalTag.Payload)
			}
			c.Directives = append(c.Directives, "@internal")
		}
		// @updatable:写面能力(依赖 dataType,在类型解析之后展开)。
		if hasUpdatable {
			if err := applyUpdatable(colWhere, &c, updatableTag.Payload); err != nil {
				return nil, false, err
			}
			c.Directives = append(c.Directives, updatableEcho(&c))
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
	if t, ok := findTag(tableTags, "default_sort"); ok {
		column, desc, err := parseDefaultSortDirective(tableWhere, t)
		if err != nil {
			return nil, false, err
		}
		spec.DefaultSort = append(spec.DefaultSort, sortDirective{Column: column, Desc: desc})
		spec.TableDirectives = append(spec.TableDirectives, fmt.Sprintf("@default_sort: %s %s", column, dirName(desc)))
	}
	// @pk:读面入选条件与无条件 tiebreaker。有 @pk 时 Update 的 Where 方法族
	// 自动包含该列的完整类型矩阵(定位方法无需额外声明)。
	if pkColumn != "" {
		spec.Tiebreaker = camelKey(pkColumn)
		spec.ReadEnabled = true
		spec.TableDirectives = append(spec.TableDirectives, "@pk: "+pkColumn)
	}
	// 隐式 sortable:@default_sort 目标列与 @pk 列直接获得排序能力,
	// 无需列级声明(排序能力只经 @dynamic 的 sort 能力位或此处隐式授予)。
	for _, ds := range spec.DefaultSort {
		if c := columnByPhysical(spec, ds.Column); c != nil {
			c.Sortable = true
		}
	}
	if spec.Tiebreaker != "" {
		for i := range spec.Columns {
			if spec.Columns[i].Key == spec.Tiebreaker {
				spec.Columns[i].Sortable = true
			}
		}
	}
	// 更新面开关只由列级 @updatable 决定:存在 set-capable 列即生成。
	spec.UpdateEnabled = len(spec.Updatables) > 0
	// 能力覆盖:@pk 自动提供定位方法;无 @pk 时 set-capable 必须有显式
	// locate-capable 列,否则更新面不可用(每次都会撞零 where 守卫)。
	hasWhereDecl := false
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if c.Key != spec.Tiebreaker && c.WhereCapable {
			hasWhereDecl = true
			break
		}
	}
	if len(spec.Updatables) > 0 && !hasWhereDecl && !spec.ReadEnabled {
		return nil, false, fmt.Errorf("%s: set-capable @updatable requires at least one locate-capable @updatable column (no @pk locator in this table)", tableWhere)
	}
	if len(spec.Updatables) == 0 && hasWhereDecl {
		return nil, false, fmt.Errorf("%s: locate-capable @updatable requires at least one set-capable @updatable column", tableWhere)
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
	if t, ok := findTag(tableTags, "filter"); ok {
		filters, err := parseFilterDirective(tableWhere, t)
		if err != nil {
			return nil, false, err
		}
		spec.Filters = filters
		spec.TableDirectives = append(spec.TableDirectives, "@filter: "+filterEcho(filters))
	}

	// 读侧指令登记:无 @pk 表只允许"无读面更新面";此处登记供 validateTable
	// 判定(含 @pk 表不受影响)。@dynamic(含 sort 能力位)、@default_sort 与
	// @scope 都要求读面。
	spec.ReadDeclared = hasDynamicDecl || len(spec.DefaultSort) > 0 || len(spec.Scope) > 0

	included := hasDynamicDecl || spec.ReadEnabled || len(spec.Updatables) > 0
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

// columnByPhysical 按物理列名查找列(未命中返回 nil)。
func columnByPhysical(spec *TableSpec, physical string) *ColumnSpec {
	for i := range spec.Columns {
		if spec.Columns[i].Physical == physical {
			return &spec.Columns[i]
		}
	}
	return nil
}

// dynamicEcho 重建 @dynamic 回显文本:sort 能力位在前,显式 op 清单按声明序
// 跟随;default_ops 授予的 op 不是 DDL 声明,不回显。
func dynamicEcho(sortToken bool, ops []string, defaultOpsGranted bool) string {
	var parts []string
	if sortToken {
		parts = append(parts, "sort")
	}
	if !defaultOpsGranted {
		parts = append(parts, ops...)
	}
	if len(parts) == 0 {
		return ""
	}
	return "@dynamic: " + strings.Join(parts, ", ")
}

func dirName(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

// applyUpdatable 在类型解析之后把 @updatable 形态落成两个能力面与
// WHERE 方法清单(auto / locate 取类型矩阵,list 取显式清单)。
func applyUpdatable(where string, c *ColumnSpec, payload string) error {
	switch c.UpdatableMode {
	case "set":
		c.SetCapable = true
	case "locate":
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
		// 枚举数组在 catalog 中的 Type.Name 仍是枚举名:非空的 @updatable
		// 会生成标量枚举的 Set/Where(类型不匹配,运行期必失败),生成期先拒绝。
		// 该限制只针对写面,不扩展读侧(@dynamic)行为。
		if col.IsArray && c.UpdatableMode != "" {
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
		if c.Hidden || c.Sortable || c.UpdatableMode != "" || len(c.Ops) > 0 {
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

// validateTable 表级校验:camel 冲突、@pk、@default_sort、sortable 类型限制、
// @scope / @filter 目标存在性、枚举数组能力面、override 与更新面 between 分叉等。
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
		// 无 @pk 纯更新面表正是 between 的主要使用形态。
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
		// 数组列一律不可作 tiebreaker:枚举数组的 DataType 同为 "enum",仅按
		// DataType 的白名单会穿透,而游标取值按标量形态发射(如 string(row.X)
		// 对切片字段不可编译)。
		if c.IsArray {
			return fmt.Errorf("table %s: @pk column %q must not be an array (tiebreaker values are embedded in the pagination cursor)", spec.Name, c.Physical)
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

	// @default_sort 目标必须存在、非 @internal 且类型可排序;排序能力由
	// @default_sort / @pk 隐式授予,无需列级声明。
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
	// 枚举分支拒绝 @updatable。检查置于入选表集:未入选表的列
	// 声明与 default_ops 策略本不生效,不因此 fail。
	for i := range spec.Columns {
		c := &spec.Columns[i]
		if c.DataType != "enum" || !c.IsArray {
			continue
		}
		if c.Sortable {
			return fmt.Errorf("column %s.%s: array type %q is not supported for sorting (@dynamic: sort or implicit @pk/@default_sort)", spec.Name, c.Physical, c.EnumPhysical)
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

	// sortable 列的类型限制:int_array(数组 seek 谓词无法参数化)、jsonb
	// (jsonb 排序按结构化比较,对前端无可解释语义)与 unmapped 不可排序,
	// jsonb 仅过滤/投影放行。numeric 可排序:游标值编码为十进制字符串,seek
	// 占位符发射 ?::numeric,比较在服务端以 numeric 精确进行。float4/smallint
	// 放行:其模型字段(float32/int16)由 dynq 游标编码分支覆盖,seek 参数按
	// number/integer 收敛为 float64/int64,PG 跨类型比较不损索引。
	for _, c := range spec.Columns {
		if !c.Sortable {
			continue
		}
		if c.DataType == "int_array" {
			return fmt.Errorf("column %s.%s: int_array columns cannot be sorted (@dynamic: sort, @pk, or @default_sort)", spec.Name, c.Physical)
		}
		if c.DataType == "jsonb" {
			return fmt.Errorf("column %s.%s: jsonb columns cannot be sorted (@dynamic: sort, @pk, or @default_sort)", spec.Name, c.Physical)
		}
		if c.DataType == "unmapped" {
			return fmt.Errorf("column %s.%s: sorting requires a mappable type (@dynamic: sort, @pk, or @default_sort)", spec.Name, c.Physical)
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
// 本检测:分叉故障类不因表形态而豁免。
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

// applyUIManifest 把 options.ui 注入入选表的列元数据。表/列键使用物理
// snake_case 名;表必须存在、被生成范围选中且有读面,列必须存在且非
// @internal,否则死配置 fail(不静默忽略)。
func applyUIManifest(schema *pb.Schema, selected []*TableSpec, opts *Options) error {
	if len(opts.UI) == 0 {
		return nil
	}
	all := make(map[string]bool, len(schema.Tables))
	for _, t := range schema.Tables {
		all[t.Rel.Name] = true
	}
	sel := make(map[string]*TableSpec, len(selected))
	for _, spec := range selected {
		sel[spec.Name] = spec
	}
	for _, tableName := range sortedKeys(opts.UI) {
		cols := opts.UI[tableName]
		if cols == nil {
			return fmt.Errorf("ui.%s: entry must not be null (dead configuration)", tableName)
		}
		if !all[tableName] {
			return fmt.Errorf("ui.%s: table not found in schema %q", tableName, opts.Schema)
		}
		spec, ok := sel[tableName]
		if !ok {
			return fmt.Errorf("ui.%s: table is not selected for generation (dead configuration)", tableName)
		}
		if !spec.ReadEnabled {
			return fmt.Errorf("ui.%s: table has no read surface (no @pk); UI metadata only applies to read-side tables", tableName)
		}
		for _, colName := range sortedKeys(cols) {
			meta := cols[colName]
			where := fmt.Sprintf("ui.%s.%s", tableName, colName)
			if meta == nil {
				return fmt.Errorf("%s: entry must not be null", where)
			}
			if meta.isEmpty() {
				return fmt.Errorf("%s: entry must not be empty (dead configuration)", where)
			}
			c := columnByPhysical(spec, colName)
			if c == nil {
				return fmt.Errorf("%s: column not found in table %q", where, tableName)
			}
			if c.Hidden {
				return fmt.Errorf("%s: @internal column must not carry UI metadata (it never enters the frontend schema)", where)
			}
			if err := applyUIColumn(where, c, meta); err != nil {
				return err
			}
		}
	}
	return nil
}

// isEmpty 报告 UI 条目未声明任何字段(对生成无影响 = 死配置)。
func (m *UIColumn) isEmpty() bool {
	return m.Label == "" && m.Desc == "" && m.Order == nil && m.Visibility == "" &&
		m.Options == nil && m.API == nil
}

// applyUIColumn 把单列的 UI 声明落到 ColumnSpec:label 缺省回落物理列名,
// visibility 缺省 optional,options / api 二选一。
func applyUIColumn(where string, c *ColumnSpec, meta *UIColumn) error {
	if meta.Label != "" {
		c.Label = meta.Label
	}
	c.Desc = meta.Desc
	c.Order = meta.Order
	vis := meta.Visibility
	if vis == "" {
		vis = "optional"
	}
	if !visibilityValues[vis] {
		return fmt.Errorf("%s: visibility must be always|optional|hidden, got %q", where, meta.Visibility)
	}
	c.Visibility = vis
	if meta.Options != nil && meta.API != nil {
		return fmt.Errorf("%s: options cannot be combined with api (one option source per column)", where)
	}
	if meta.Options != nil {
		opts, err := typedUIOptions(where, c.DataType, meta.Options)
		if err != nil {
			return err
		}
		c.Options = opts
	}
	if meta.API != nil {
		api, err := validateUIAPI(where, c.DataType, meta.API)
		if err != nil {
			return err
		}
		c.API = api
	}
	return nil
}

// typedUIOptions 把 manifest 的 options 列表按列 dataType 定型为 OptionSpec。
// value 以 json.RawMessage 保留原始形态,禁止 float64 中转;重复 value fail。
func typedUIOptions(where, dataType string, items []UIOption) ([]OptionSpec, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("%s.options: at least one item is required", where)
	}
	out := make([]OptionSpec, 0, len(items))
	seen := map[string]bool{}
	for i, item := range items {
		if item.Label == "" {
			return nil, fmt.Errorf("%s.options[%d]: label must be non-empty", where, i)
		}
		val, err := typedOptionValue(dataType, item.Value)
		if err != nil {
			return nil, fmt.Errorf("%s.options[%d]: %v", where, i, err)
		}
		key := fmt.Sprint(val)
		if seen[key] {
			return nil, fmt.Errorf("%s.options[%d]: value %s declared twice", where, i, key)
		}
		seen[key] = true
		out = append(out, OptionSpec{Value: val, Label: item.Label})
	}
	return out, nil
}

// typedOptionValue 按列 dataType 解析 value:integer → int64,
// string/uuid → string;其他 dataType 不支持静态字典。
func typedOptionValue(dataType string, raw json.RawMessage) (any, error) {
	switch dataType {
	case "integer":
		// 用 UseNumber 保留原始数字字面量,禁止 float64 中转;JSON 字符串
		// 不是合法整数(与 string 列的书写形态二分)。
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("value must be an integer (column dataType is integer), got %s", string(raw))
		}
		num, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("value must be an integer (column dataType is integer), got %s", string(raw))
		}
		n, err := strconv.ParseInt(num.String(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value %s is not a valid integer (column dataType is integer)", num.String())
		}
		return n, nil
	case "string", "uuid":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("value must be a string (column dataType is %s), got %s", dataType, string(raw))
		}
		return s, nil
	default:
		return nil, fmt.Errorf("options not supported for dataType %s (want integer|uuid|string fields)", dataType)
	}
}

// validateUIAPI 校验 manifest 的 api 声明并转为 APISpec。
func validateUIAPI(where, dataType string, meta *UIAPI) (*APISpec, error) {
	switch dataType {
	case "integer", "uuid", "string":
	default:
		return nil, fmt.Errorf("%s.api: not supported for dataType %s (want integer|uuid|string reference fields)", where, dataType)
	}
	if meta.Endpoint == "" {
		return nil, fmt.Errorf("%s.api: endpoint is required", where)
	}
	if strings.ContainsAny(meta.Endpoint, "?") {
		return nil, fmt.Errorf("%s.api: endpoint must not contain a query string, got %q", where, meta.Endpoint)
	}
	if strings.Contains(meta.Endpoint, "=") {
		return nil, fmt.Errorf("%s.api: endpoint must be a path, got %q", where, meta.Endpoint)
	}
	if meta.ValueKey == "" || meta.LabelKey == "" {
		return nil, fmt.Errorf("%s.api: value_key and label_key are required", where)
	}
	switch meta.Mode {
	case "", "single", "multi", "search":
	default:
		return nil, fmt.Errorf("%s.api: mode must be single|multi|search, got %q", where, meta.Mode)
	}
	return &APISpec{Endpoint: meta.Endpoint, ValueKey: meta.ValueKey, LabelKey: meta.LabelKey, Mode: meta.Mode}, nil
}

// sortedKeys 返回 map 的键并按字典序排序,保证 manifest 校验报错稳定。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
