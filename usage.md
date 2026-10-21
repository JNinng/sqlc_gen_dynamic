# 外部工程接入指南

sqlc_gen_dynamic 支持两种接入形态,sqlc.yaml 的 `plugins` 块二选一:

- **wasm**(推荐):锁定 GitHub Release 发布资产(`url` + `sha256`),`sqlc generate` 时自动下载校验,本机无需构建插件;
- **process(本地二进制)**:指向本地插件可执行文件,适合开发调试与内网环境。

运行期函数来自公开 Go 模块 `github.com/jninng/sqlc_gen_dynamic` 的 `dynq` 包,与插件同 tag 发布、同版本配套。注释 DSL 的声明方式(语法、列级 / 表级指令、类型映射、能力矩阵、预设展开、校验规则)见 [dsl.md](./dsl.md);查询入参 / 响应协议与调用契约见 [README](./README.md),行级数据权限机制见 [scope.md](./scope.md),前端 Schema 契约见 [schema.md](./schema.md),设计原理见 [desc.md](./desc.md)。

## 前置条件

- sqlc ≥ v1.31
- Go 模块依赖(运行期 dynq):`go get github.com/jninng/sqlc_gen_dynamic@vX.Y.Z`,版本与所接入的插件一致

## sqlc.yaml 接线(双 sql 块)

一个 `sql` 块只挂一个 codegen,因此配置**两个块共享同一份 schema.sql**(迁移文件即 DDL 单一事实源):块 1 sqlc-gen-go 产出模型与固定查询;块 2 本插件仅消费 Catalog 生成动态 List(`queries` 字段 sqlc 要求必填但插件忽略)。

### plugins 块:wasm 形态

```yaml
plugins:
  - name: "sqlc_gen_dynamic"
    wasm:
      url: "https://github.com/JNinng/sqlc_gen_dynamic/releases/download/v0.2.0/sqlc_gen_dynamic-v0.2.0.wasm"
      sha256: "0b71fc8bc02c93085119e82b1824398aa250c03715dd85e4ff8f78c92f35dac7"
    env:                       # 可选:透传 DYNQ_DUMP_REQUEST 可把 GenerateRequest dump 落盘排查
      - "DYNQ_DUMP_REQUEST"
```

发布资产为每个 Release 的 `sqlc_gen_dynamic-vX.Y.Z.wasm` 与同名 `.sha256`(由本仓库 `make release-wasm` 流程产出,wasip1 编译 + sha256)。`sha256` 必须与 wasm 资产字节一致——取 Release 的 `.sha256` 资产,或本地 `make release-wasm` 后 `sha256sum` 比对;不匹配时 sqlc generate 拒绝执行。sqlc 按 url 下载并校验,首次之后缓存于用户缓存目录,后续生成离线可用。

### plugins 块:process 形态(本地二进制)

```yaml
plugins:
  - name: "sqlc_gen_dynamic"
    process:
      cmd: "bin/sqlc_gen_dynamic"   # 相对 sqlc 命令的运行目录解析(exec.LookPath)
    env:
      - "DYNQ_DUMP_REQUEST"
```

二进制获取二选一:

```bash
# 从已发布模块安装(落入 GOBIN;版本与 go.mod 的 dynq 依赖保持一致)
go install github.com/jninng/sqlc_gen_dynamic/tools/sqlc_gen_dynamic@vX.Y.Z

# 或本仓源码构建(本仓开发态即此形态,make sqlc 的第一步)
go build -o bin/sqlc_gen_dynamic ./tools/sqlc_gen_dynamic      # Linux/macOS
go build -o bin/sqlc_gen_dynamic.exe ./tools/sqlc_gen_dynamic  # Windows:必须带 .exe,
                                                               # 否则 sqlc process 插件报 not found
```

### sql 块(两形态相同)

```yaml
sql:
  - engine: "postgresql"
    schema: "schema.sql"
    queries: "queries.sql"
    gen:
      go:
        package: "gen"
        out: "gen"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_db_tags: true
        emit_pointers_for_null_types: true
        emit_interface: true

  - engine: "postgresql"
    schema: "schema.sql"
    queries: "queries.sql"
    codegen:
      - plugin: "sqlc_gen_dynamic"
        out: "gen"
        options:
          package: "gen"
          runtime_import: "github.com/jninng/sqlc_gen_dynamic/dynq"
          schema: "public"
          default_ops: "none"
          page_default: 20
          page_max: 200
```

动态表声明写在 schema.sql 的 `COMMENT ON` 中(列级 `@dynamic` / `@sortable` / `@hideable` / `@internal` / `@api`,表级 `@default_sort` / `@page` / `@pk` / `@scope`;旧名 `@selectable` / `@hidden` 为兼容期别名,生成期有 deprecation 提示);DSL 语法、能力矩阵、预设展开与 fail 条件全集见 [dsl.md](./dsl.md),非法声明为生成期 fail,报错定位到具体列。

### codegen options

| option | 说明 |
|---|---|
| `package` | 产物包名(必填;与块 1 产物同包时须一致) |
| `model_import` / `model_package` | 块 1 产物位置(跨包引用其模型类型与 DBTX 时必填,见下节) |
| `schema` | 目标 schema,默认 `public` |
| `tables` | 生成范围;缺省为该 schema 下所有含 `@dynamic` 或 `@pk` 声明的表 |
| `default_ops` | 未标注列的策略,默认 `none`(显式声明才生成,防 Params 字段爆炸) |
| `go_type_overrides` | 逃生舱,镜像 sqlc-gen-go 的类型覆盖 |
| `runtime_import` | dynq 运行时包导入路径 |
| `page_default` / `page_max` | 分页全局默认(表注释 `@page` 可覆盖),默认 20/200 |

## 产物布局:同包与跨包

- **同包**(推荐起步):两个 sql 块 `out` 同目录、`package` 同名。插件产物直接引用块 1 的模型与 DBTX,无需 `model_import`;每张动态表产出一份 `<table>_dynamic_gen.go`(registry / 字段常量 / Params / Parse / Schema / List / Count),与 sqlc-gen-go 产物同包共存。
- **跨包**:块 2 options 追加 `model_import`(块 1 产物 import 路径)与 `model_package`(包名,作限定前缀),插件产物独立成包并跨包引用模型类型。

## 升级

wasm 形态顺序固定(插件 proto 与 sqlc 二进制存在版本耦合,见 desc.md §4.5):

1. `go get github.com/jninng/sqlc_gen_dynamic@vX.Y.Z`(运行期 dynq);
2. 更新 sqlc.yaml 的 wasm `url` + `sha256` 至同一版本;
3. `sqlc generate` 全量再生成,`git diff` 评审生成物;
4. CI 固定 sqlc 版本,保证锁漂移检查可复现。

process 形态无资产锁定:重新 `go install` 或构建新版本二进制后,下次生成即用新插件(`cmd` 配置不变)。插件二进制与 go.mod 的 dynq 依赖仍须同版本——生成模板引用的 dynq API 由同版本配套保证,不得跨版本混用。
