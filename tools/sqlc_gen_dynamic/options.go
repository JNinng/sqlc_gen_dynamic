package main

import (
	"encoding/json"
	"fmt"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// Options 是 sqlc.yaml 中 codegen.options 的形态(§4.4)。
type Options struct {
	// Package 产物包名(与块 1 sqlc-gen-go 输出同包时须一致)。
	Package string `json:"package"`
	// ModelImport 块 1 产物 import 路径;空 = 与本插件产物同包。
	ModelImport string `json:"model_import"`
	// ModelPackage 块 1 产物包名;用于跨包引用时的限定前缀(如 "models.")。
	ModelPackage string `json:"model_package"`
	// Schema 目标 schema,默认 public。
	Schema string `json:"schema"`
	// Tables 生成范围;缺省为该 schema 下所有含 @dynamic 声明的表。
	Tables []string `json:"tables"`
	// DefaultOps 未标注列的策略:none | exact | in | range | fuzzy | has。
	// 默认 none(显式声明才生成,防 Params 字段爆炸)。
	DefaultOps string `json:"default_ops"`
	// GoTypeOverrides 逃生舱,镜像 sqlc-gen-go 的类型覆盖。
	GoTypeOverrides []GoTypeOverride `json:"go_type_overrides"`
	// RuntimeImport dynq 运行时包导入路径。
	RuntimeImport string `json:"runtime_import"`
	// PageDefault / PageMax 分页全局默认(表注释 @page 可覆盖)。
	PageDefault int `json:"page_default"`
	PageMax     int `json:"page_max"`
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
		PageDefault:   20,
		PageMax:       200,
	}
	if len(req.PluginOptions) > 0 {
		if err := json.Unmarshal(req.PluginOptions, opts); err != nil {
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
	case "none", "exact", "in", "range", "fuzzy", "has":
	default:
		return nil, fmt.Errorf("invalid default_ops %q (want none|exact|in|range|fuzzy|has)", opts.DefaultOps)
	}
	if opts.PageDefault < 1 {
		opts.PageDefault = 20
	}
	if opts.PageMax < 1 {
		opts.PageMax = 200
	}
	if opts.PageDefault > opts.PageMax {
		return nil, fmt.Errorf("page_default %d exceeds page_max %d", opts.PageDefault, opts.PageMax)
	}
	if opts.ModelImport != "" && opts.ModelPackage == "" {
		return nil, fmt.Errorf("model_package is required when model_import is set")
	}
	return opts, nil
}

const defaultRuntimeImport = "github.com/jninng/sqlc_gen_dynamic/dynq"
