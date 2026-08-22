// sqlc_gen_dynamic 是 sqlc 自定义 codegen 插件:从 Catalog 的 DDL 注释 DSL 生成
// 动态单表 List 代码(registry / 字段常量 / Params / Parse / Schema / List / Count)。
//
// 双构建模式(process / wasm)共用同一协议:stdin 收 GenerateRequest protobuf,
// stdout 回 GenerateResponse protobuf;由 plugin-sdk-go 的 codegen.Run 承载。
package main

import (
	"github.com/sqlc-dev/plugin-sdk-go/codegen"
)

func main() {
	codegen.Run(Generate)
}
