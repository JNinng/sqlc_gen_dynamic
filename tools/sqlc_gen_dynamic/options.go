package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// Options 是 sqlc.yaml 中 codegen.options 的形态(§4.4)。
// 解码严格:未知键 → 生成期 fail(不设兼容期,避免拼写错误静默失效)。
type Options struct {
	// Package 产物包名(与块 1 sqlc-gen-go 输出同包时须一致)。
	Package string `json:"package"`
	// ModelImport 块 1 产物 import 路径;空 = 与本插件产物同包。
	ModelImport string `json:"model_import"`
	// ModelPackage 块 1 产物包名;用于跨包引用时的限定前缀(如 "models.")。
	ModelPackage string `json:"model_package"`
	// Schema 目标 schema,默认 public。
	Schema string `json:"schema"`
	// Tables 生成范围;缺省为该 schema 下所有含 @dynamic / @pk / set-capable @updatable 声明的表。
	Tables []string `json:"tables"`
	// DefaultOps 未标注列的策略:none | exact | membership | range | fuzzy。
	// 默认 none(显式声明才生成,防 Params 字段爆炸)。
	DefaultOps string `json:"default_ops"`
	// GoTypeOverrides 逃生舱,镜像 sqlc-gen-go 的类型覆盖。
	GoTypeOverrides []GoTypeOverride `json:"go_type_overrides"`
	// RuntimeImport dynq 运行时包导入路径。
	RuntimeImport string `json:"runtime_import"`
	// Page 分页策略:全局 default/max 与表级覆盖,未声明的字段回落全局值。
	Page PageOptions `json:"page"`
	// UIDefaults 是前端 UI 元数据的跨表兜底:物理列名 → 列级 UI 声明。
	// 对每张入选读面表中同名且非 @internal 的列生效;ui 的表级条目按字段
	// 覆盖兜底值(未声明的字段继承)。未被任何可见列命中的条目是死配置。
	UIDefaults map[string]*UIColumn `json:"ui_defaults"`
	// UI 是前端 UI 元数据:物理表名 → 物理列名 → 列级 UI 声明。
	// 协议可见性仍由 DDL 注释的 @internal 表达(@internal 列不接受 UI 条目)。
	UI map[string]map[string]*UIColumn `json:"ui"`

	// globalPageDefault / globalPageMax 是解析后的全局分页策略(内置默认
	// 20/200 打底,合法区间已在 ParseOptions 校验)。
	globalPageDefault int
	globalPageMax     int
}

// PageOptions 是分页策略。分页是产品与防护策略,不与列定义耦合:
// 全局 default/max 打底,tables 按物理表名覆盖;未声明的字段回落全局值。
type PageOptions struct {
	Default *int                    `json:"default"`
	Max     *int                    `json:"max"`
	Tables  map[string]PageOverride `json:"tables"`
}

// PageOverride 是单表分页覆盖;未声明的字段回落全局值。
type PageOverride struct {
	Default *int `json:"default"`
	Max     *int `json:"max"`
}

const (
	builtinPageDefault = 20
	builtinPageMax     = 200
)

// UIColumn 是 options.ui 中一列的 UI 元数据。仅作用于前端 schema,
// 不参与服务端校验,也不改变 List 的默认投影。
type UIColumn struct {
	Label      string     `json:"label"`
	Desc       string     `json:"desc"`
	Order      *int       `json:"order"`
	Visibility string     `json:"visibility"` // always | optional | hidden;空 = optional
	Options    []UIOption `json:"options"`
	API        *UIAPI     `json:"api"`
}

// UIOption 是静态字典项。Value 保留原始 JSON 形态(整数/字符串),生成期
// 按列 dataType 定型;禁止 float64 中转(整数精度与类型歧义)。
type UIOption struct {
	Value json.RawMessage `json:"value"`
	Label string          `json:"label"`
}

// UIAPI 是远程选项数据源声明。
type UIAPI struct {
	Endpoint string `json:"endpoint"`
	ValueKey string `json:"value_key"`
	LabelKey string `json:"label_key"`
	Mode     string `json:"mode"` // single | multi | search;空 = 由 ops 推导
}

// GoTypeOverride 按列名或 db 类型覆盖 Params 的 Go 类型。
type GoTypeOverride struct {
	// Column 形如 "users.balance";与 DbType 二选一。
	Column string       `json:"column"`
	DbType string       `json:"db_type"`
	GoType ParsedGoType `json:"go_type"`
}

// ParsedGoType 镜像 sqlc 的 go_type 覆盖形态。
type ParsedGoType struct {
	Import   string `json:"import"`
	Package  string `json:"package"`
	TypeName string `json:"type_name"`
}

// ParseOptions 解析并校验插件 options。
func ParseOptions(req *pb.GenerateRequest) (*Options, error) {
	opts := &Options{
		Schema:        "public",
		DefaultOps:    "none",
		RuntimeImport: defaultRuntimeImport,
	}
	if len(req.PluginOptions) > 0 {
		dec := json.NewDecoder(bytes.NewReader(req.PluginOptions))
		dec.DisallowUnknownFields()
		if err := dec.Decode(opts); err != nil {
			return nil, fmt.Errorf("unmarshal plugin options: %w", err)
		}
	}
	if opts.Package == "" {
		return nil, fmt.Errorf("option \"package\" is required")
	}
	if opts.RuntimeImport == "" {
		opts.RuntimeImport = defaultRuntimeImport
	}
	if opts.Schema == "" {
		opts.Schema = "public"
	}
	switch opts.DefaultOps {
	case "none", "exact", "membership", "range", "fuzzy":
	default:
		return nil, fmt.Errorf("invalid default_ops %q (want none|exact|membership|range|fuzzy)", opts.DefaultOps)
	}
	def, max, err := opts.Page.global()
	if err != nil {
		return nil, err
	}
	opts.globalPageDefault, opts.globalPageMax = def, max
	if opts.ModelImport != "" && opts.ModelPackage == "" {
		return nil, fmt.Errorf("model_package is required when model_import is set")
	}
	// 反向死配置:model_package 单独设置时 qualify 会忽略它,模型引用不带
	// 前缀,而 override 的显式 package 前缀仍会进产物——引用悬空且语义误导,
	// 显式拒绝而非静默忽略。
	if opts.ModelPackage != "" && opts.ModelImport == "" {
		return nil, fmt.Errorf("model_package is set but model_import is empty; model_package only qualifies cross-package references")
	}
	// override 与 model_import 同包前置校验:models import 在产物中只声明
	// 一次,import 一致而 package 不一致(或反之)会让别名与类型前缀失配,
	// 错误落在产物编译期;在此显式拒绝。model_package 单独设置(无
	// model_import)是被 qualify 忽略的死配置,不参与比较。
	for i := range opts.GoTypeOverrides {
		o := &opts.GoTypeOverrides[i]
		if o.GoType.Import == "" || opts.ModelImport == "" {
			continue
		}
		pkg := o.GoType.Package
		if pkg == "" {
			pkg = o.GoType.Import[strings.LastIndexByte(o.GoType.Import, '/')+1:]
		}
		if (o.GoType.Import == opts.ModelImport) != (pkg == opts.ModelPackage) {
			return nil, fmt.Errorf("go_type_overrides[%d]: import/package %q/%q conflicts with model_import/model_package %q/%q (the models import is emitted once; align the package or use a distinct one)",
				i, o.GoType.Import, pkg, opts.ModelImport, opts.ModelPackage)
		}
	}
	// override 之间的别名一致性前置校验:同一 import 路径在产物中只 import
	// 一次,两条声明给出不同别名时,按路径收集的 import 与其中一列的类型
	// 前缀失配(后者静默覆盖前者),错误落在产物编译期;在此显式拒绝。
	pkgByPath := map[string]string{}
	for i := range opts.GoTypeOverrides {
		o := &opts.GoTypeOverrides[i]
		if o.GoType.Import == "" {
			continue
		}
		pkg := o.GoType.Package
		if pkg == "" {
			pkg = o.GoType.Import[strings.LastIndexByte(o.GoType.Import, '/')+1:]
		}
		if prev, ok := pkgByPath[o.GoType.Import]; ok {
			if prev != pkg {
				return nil, fmt.Errorf("go_type_overrides[%d]: import %q is declared with conflicting aliases %q and %q; one import path must map to one alias in the generated package",
					i, o.GoType.Import, prev, pkg)
			}
			continue
		}
		pkgByPath[o.GoType.Import] = pkg
	}
	return opts, nil
}

// global 返回全局分页策略(未声明取内置默认)并校验 1 ≤ default ≤ max。
func (p PageOptions) global() (def, max int, err error) {
	def, max = builtinPageDefault, builtinPageMax
	if p.Default != nil {
		def = *p.Default
	}
	if p.Max != nil {
		max = *p.Max
	}
	if err := validatePageRange(def, max, "page"); err != nil {
		return 0, 0, err
	}
	return def, max, nil
}

// forTable 返回某表的分页策略:全局值打底,表级覆盖优先。
func (p PageOptions) forTable(table string, globalDef, globalMax int) (def, max int, ok bool, err error) {
	ov, ok := p.Tables[table]
	if !ok {
		return globalDef, globalMax, false, nil
	}
	def, max = globalDef, globalMax
	if ov.Default != nil {
		def = *ov.Default
	}
	if ov.Max != nil {
		max = *ov.Max
	}
	if err := validatePageRange(def, max, "page.tables."+table); err != nil {
		return 0, 0, true, err
	}
	return def, max, true, nil
}

func validatePageRange(def, max int, where string) error {
	if def < 1 {
		return fmt.Errorf("%s.default must be a positive integer, got %d", where, def)
	}
	if max < 1 {
		return fmt.Errorf("%s.max must be a positive integer, got %d", where, max)
	}
	if def > max {
		return fmt.Errorf("%s.default %d exceeds %s.max %d", where, def, where, max)
	}
	return nil
}

// visibilityValues 是 visibility 的封闭取值集合。
var visibilityValues = map[string]bool{"always": true, "optional": true, "hidden": true}

const defaultRuntimeImport = "github.com/jninng/sqlc_gen_dynamic/dynq"
