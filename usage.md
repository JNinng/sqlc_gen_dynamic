# 外部工程接入指南

sqlc_gen_dynamic 支持两种接入形态,sqlc.yaml 的 `plugins` 块二选一:

- **wasm**(推荐):锁定 GitHub Release 发布资产(`url` + `sha256`),`sqlc generate` 时自动下载校验,本机无需构建插件;
- **process(本地二进制)**:指向本地插件可执行文件,适合开发调试与内网环境。

运行期函数来自公开 Go 模块 `github.com/jninng/sqlc_gen_dynamic` 的 `dynq` 包,与插件同 tag 发布、同版本配套。注释 DSL 的声明方式(语法、列级 / 表级指令、类型映射、能力矩阵、预设展开、校验规则)见 [dsl.md](./dsl.md);查询入参 / 响应协议与调用契约见 [README](./README.md),行级数据权限机制见 [scope.md](./scope.md),前端 Schema 契约见 [schema.md](./schema.md),设计原理见 [desc.md](./desc.md)。

## 前置条件

- sqlc ≥ v1.31
- Go 模块依赖(运行期 dynq):`go get github.com/jninng/sqlc_gen_dynamic@vX.Y.Z`,版本与所接入的插件一致

## sqlc.yaml 接线(双 sql 块)

一个 `sql` 块只挂一个 codegen,因此配置**两个块共享同一份 schema.sql**(迁移文件即 DDL 单一事实源):块 1 sqlc-gen-go 产出模型与固定查询;块 2 本插件仅消费 Catalog 生成动态 List / typed Update(`queries` 字段 sqlc 要求必填但插件忽略)。

### plugins 块:wasm 形态

```yaml
plugins:
  - name: "sqlc_gen_dynamic"
    wasm:
      url: "https://github.com/JNinng/sqlc_gen_dynamic/releases/download/v0.2.1/sqlc_gen_dynamic-v0.2.1.wasm"
      sha256: "6d34fda5f84ae83926031656a2eba4a8f4bd7b64902cd888370d6cb30ae96eae"
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
          # 分页策略:全局 default/max + 表级覆盖(表级优先):
          page:
            default: 20
            max: 200
            tables:
              orders: {default: 20, max: 100}   # 表级覆盖优先
          # 前端 UI 元数据:物理表名 → 物理列名 → label / desc / order / visibility /
          # options / api。跨表同名审计字段(created_at 等)用 ui_defaults 兜底:
          ui_defaults:
            created_at: {label: 创建时间, visibility: hidden}
          ui:
            users:
              id: {visibility: always}
              email: {label: 电子邮箱, order: 10}
              age:
                order: -1
                options:
                  - {value: 0, label: 未知}
                  - {value: 18, label: 成年}
              meta: {visibility: hidden}
            orders:
              user_id:
                api: {endpoint: /api/v1/users/options, value_key: id, label_key: name}
```

**注释只承载能力指令**:列级 `@dynamic`(含 `sort` 能力位)/ `@internal` / `@updatable`,表级 `@default_sort` / `@pk` / `@scope` / `@filter`。label / desc / order / visibility / options / api 等 UI 元数据走 `codegen.options.ui`(跨表同名审计字段可写 `options.ui_defaults` 兜底,见下),注释里的人类文本不参与生成。分页策略走 `options.page`。旧指令(`@sortable` / `@hideable` / `@selectable` / `@hidden` / `@page` / `@update` / `@api` / `@label` / `@desc` / `@order` / `@options`)与旧拼写不做兼容解析,一律生成期 fail。DSL 语法、能力矩阵、预设展开与 fail 条件全集见 [dsl.md](./dsl.md)。

### codegen options

| option | 说明 |
|---|---|
| `package` | 产物包名(必填;与块 1 产物同包时须一致) |
| `model_import` / `model_package` | 块 1 产物位置(跨包引用其模型类型与 DBTX 时必填,见下节);二者必须成对出现,单独设置 → 生成期 fail。`go_type_overrides` 与 models 同包时 import/package 需一致,不一致 → 生成期 fail |
| `schema` | 目标 schema,默认 `public` |
| `tables` | 生成范围;缺省为该 schema 下所有含 `@dynamic` / `@pk` / set-capable `@updatable` 声明的表 |
| `default_ops` | 未标注列的策略,默认 `none`(显式声明才生成,防 Params 字段爆炸);只作用于含 `@pk` 的读面,无 `@pk` 的入选表 + 非 `none` → 生成期 fail |
| `go_type_overrides` | 逃生舱,镜像 sqlc-gen-go 的类型覆盖(`column` 形态按 `table.column` 精确限定,不跨表匹配同名列);带 `import` 的覆盖会把外部包收进产物 import(`package` 缺省取路径末段)。override 类型与协议门收敛类型分叉时,读面值 op(`eq`/`in`/`has` 等引用列 GoType 的槽位)与更新面 `between`(边界比较按默认类型发射:数值 `lo > hi`、时间型 `lo.After(hi)`;无 @pk 纯更新面表同样拒绝)→ 生成期 fail;更新面 Set / Where 与模糊三 op 不受限。import/别名冲突前置校验:同一 import 路径声明不同别名、同一生成文件内别名与其他 override 或该文件固定 import(sq / dynq / pgx / time / json / context / models)撞名 → 生成期 fail(别名落在不同表的文件中各自合法,import 别名是文件作用域) |
| `runtime_import` | dynq 运行时包导入路径 |
| `page` | 分页策略:`{default, max, tables}`。`default`/`max` 为全局值(默认 20/200),`tables` 按物理表名覆盖(表级覆盖优先,未声明的字段回落全局值);校验 `1 ≤ default ≤ max`,`tables` 指向不存在或未入选的表 → 生成期 fail |
| `ui` | 前端 UI 元数据:`{<表>: {<列>: {label, desc, order, visibility, options, api}}}`。键用物理表名/列名(snake_case);表必须存在、被生成范围选中且有读面,列必须存在且非 `@internal`,否则死配置 fail。未知键严格解码 fail。空 `label` 回落物理列名 |
| `ui_defaults` | 跨表 UI 兜底:`{<列>: {label, desc, order, visibility, options, api}}`,键为物理列名。对每张入选读面表中同名且非 `@internal` 的列生效,无该列的表跳过;`ui.<表>.<列>` 按字段覆盖(未声明字段继承兜底),`options` 与 `api` 是同一选项来源槽位(表级声明其一即替换兜底来源)。兜底条目必须至少命中一个可见列,否则死配置 fail;同名但 dataType 不同的表按该表实际类型校验,失败定位到 `ui_defaults.<列> (as <表>.<列>)` |
| `ui.*.visibility` | 前端列表初始显隐:`always`(固定显示)/ `optional`(默认显示、可切换,缺省)/ `hidden`(默认收起、可展开)。只影响前端渲染,不参与服务端校验,不改变默认投影;`hidden` 列仍会被默认查询返回 |
| `ui.*.options` | 静态字典:`[{value, label}]`;value 按列 dataType 定型(integer → 整数,string/uuid → 字符串),重复 value / 空 label / 类型不符 → 生成期 fail |
| `ui.*.api` | 远程选项数据源:`{endpoint, value_key, label_key, mode?}`;仅 `integer`/`uuid`/`string` 列,与 `options` 同列互斥 |
| `ui.*.order` | 前端 schema `fields` 排序序号(可负、可同号;未标注保持 DDL 声明序) |

## 产物布局:同包与跨包

- **同包**(推荐起步):两个 sql 块 `out` 同目录、`package` 同名。插件产物直接引用块 1 的模型与 DBTX,无需 `model_import`;每张动态表产出一份 `<table>_dynamic_gen.go`(有 `@pk` 时含读面 registry / 字段常量 / Params / Parse / Schema / List / Count,set-capable `@updatable` 列另含 typed 更新面;**无 `@pk` 表只产更新面**,不生成读侧产物),与 sqlc-gen-go 产物同包共存。命名空间对象取 `<表名单数>Update`(而非单数名,模型类型已占用);`UpdateXxx` 为包级函数,sqlc 静态查询为 `*Queries` 方法,同名可共存(同名仅影响阅读,可按需给 sqlc query 改名);`<单数>Table` / `<单数>Update` / `<单数>UpdateOption` 与任一 sqlc 模型类型撞名时生成期 fail。
- **跨包**:块 2 options 追加 `model_import`(块 1 产物 import 路径)与 `model_package`(包名,作限定前缀),插件产物独立成包并跨包引用模型类型。

## 迁移(无兼容期)

旧指令与旧名一律生成期 fail;旧 `sqlc.yaml` options 键、`dynq` API 字段与前端 schema 键均不做兼容映射。**完整迁移指南(旧→新对照表、残留扫描命令、验证清单、回滚与 FAQ)见根目录 [MIGRATION.md](./MIGRATION.md)**;要点如下:

1. **UI 指令迁到 `options.ui`**:删除注释里的 `@label` / `@desc` / `@order` / `@options` / `@api` 与 `@hideable` / `@selectable`,按物理表名/列名写入 `options.ui`。原先 `@hideable` 的列写 `visibility: optional`(缺省即此值),固定显示列写 `always`,默认收起写 `hidden`;v0.5 及更早版本中**未标注 `@hideable`** 的列旧行为是默认不展示(`hideable=false`),新契约缺省变成默认显示、可切换的 `optional`——要保留"默认不展示"写 `visibility: hidden`,要固定显示写 `visibility: always`。label 缺省回落物理列名(注释里的人类文本不再参与生成)。
2. **分页迁到 `options.page`**:删除表级 `@page: <default>/<max>`,改在 `options.page` 写全局 `default`/`max`,表级差异放 `tables.<表名>`;删除 `page_default` / `page_max`。
3. **删除 `@update`**:更新面开关只由 set-capable `@updatable` 列决定;`@update: false` 改为移除列上的 `@updatable`。
4. **`@sortable` 合并为 `@dynamic` 的 `sort` 能力位**:`[@sortable]` → `[@dynamic: sort]`;`@pk` 列与 `@default_sort` 目标列已隐式可排序,可删掉显式声明。
5. **预设更名**:`in` → `membership`(展开仍为 `in + not_in`);裸 `in` 现在只是 `in` op,依赖 `not_in` 的声明必须改写。`has` 预设删除,jsonb 列显式写 `has` op。
6. **`@updatable` 形态收敛**:`auto` → 裸 `@updatable`;`where` → `locate`;`false` → 直接删除指令;`@pk` 列上的任何 `@updatable` 一律删除(主键不可写)。
7. **旧名 `@hidden` 删除**:改写为 `@internal`(服务端内部列,协议面永不可见;与 UI 显隐无关)。
8. **前端 schema 键**:`hideable` / `selectable` 两键替换为 `visibility`(三态,缺省 `optional`);`dynq.FieldMeta.Selectable` 改为 `Visibility`。
9. `sqlc generate` 全量再生成,按生成期 fail 逐条清理残留声明。

## 升级

wasm 形态顺序固定(插件 proto 与 sqlc 二进制存在版本耦合,见 desc.md §4.5):

1. `go get github.com/jninng/sqlc_gen_dynamic@vX.Y.Z`(运行期 dynq);
2. 更新 sqlc.yaml 的 wasm `url` + `sha256` 至同一版本;
3. `sqlc generate` 全量再生成,`git diff` 评审生成物;
4. CI 固定 sqlc 版本,保证锁漂移检查可复现。

process 形态无资产锁定:重新 `go install` 或构建新版本二进制后,下次生成即用新插件(`cmd` 配置不变)。插件二进制与 go.mod 的 dynq 依赖仍须同版本——生成模板引用的 dynq API 由同版本配套保证,不得跨版本混用。
