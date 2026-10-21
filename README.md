# sqlc_gen_dynamic

sqlc 自定义 codegen 插件:从 DDL 注释 DSL 生成**动态单表 List 查询代码**(运行期组合 WHERE / ORDER BY / 分页 / 返回列),与 sqlc-gen-go 的固定形态查询互补。配套运行时库 `dynq` 与示例工程 `example/`。DSL 完整参考见 [dsl.md](./dsl.md);行级数据权限见 [scope.md](./scope.md);前端 Schema 契约见 [schema.md](./schema.md);设计文档见 [desc.md](./desc.md);外部工程接入见 [usage.md](./usage.md)。

## 环境要求

- Go 1.26+
- sqlc ≥ v1.31(插件协议 `GenerateRequest` 自 v1.24 起)
- 本地 PostgreSQL(仅演示 `go run ./example/db` 需要,测试套件无数据库依赖;演示自动重建独立测试库 `sqlc_gen_dynamic_test`,可用 `DYNQ_TEST_ADMIN_DSN` 覆盖管理连接)

## 执行命令

所有命令在仓库根目录执行:

```bash
# 构建插件 + 全量生成(写 example/db/gen/,含 sqlc-gen-go 与本插件产物)
make sqlc

# 全量测试:dynq 单测 + 插件 golden 测试(无数据库依赖;example 包随测编译)
make test

# 动态查询演示:启动即重建测试库 → 应用 schema → 种子数据 → 跑 10 组查询演示
# (过滤/冲突拒绝/offset 分页/游标翻页/少量字段选择/opts 禁用/schema/跨表游标互斥/@scope 行级数据权限/in_any 数组三态)
go run ./example/db

# 生成后 git diff 锁 gen 目录,防 schema 漂移(CI 用固定版本 sqlc 执行)
make check

# 编译 wasm 产物 + sha256(发布期 sqlc.yaml 以 url+sha256 锁定插件)
make release-wasm

# 清理构建产物
make clean
```

无 make 环境时等价命令:

```bash
# make sqlc
go build -o bin/sqlc_gen_dynamic ./tools/sqlc_gen_dynamic
sqlc generate -f example/db/sqlc.yaml

# make test
go test ./...
go -C example test ./...

# make release-wasm
GOOS=wasip1 GOARCH=wasm go build -o bin/sqlc_gen_dynamic.wasm ./tools/sqlc_gen_dynamic
sha256sum bin/sqlc_gen_dynamic.wasm
```

## 生成产物

`make sqlc` 后:

- `example/db/gen/models.go`、`queries.sql.go`、`db.go` 等:sqlc-gen-go 产物(模型与固定查询)
- `example/db/gen/*_dynamic_gen.go`:本插件产物(registry / 字段常量 / Params / Parse / Schema / List / Count)

## 注释 DSL(生成策略)

> 完整参考(语法规则、列级 / 表级指令、PG 类型 → dataType 映射、能力矩阵、预设展开、操作符语义、校验规则与 fail 条件全集)见 [dsl.md](./dsl.md)。

生成策略全部声明在 DDL 注释中,随迁移版本化。指令必须包裹在 `[ ]` 中且内容以 `@` 起始;不以 `@` 起始的方括号是 label 字面文本(人类注释不误伤)。指令分列级(`@dynamic` / `@sortable` / `@selectable` / `@hidden` / `@api`)与表级(`@default_sort` / `@page` / `@pk` / `@scope`)两类,涵盖过滤 / 排序 / 返回列选择 / 分页 / 行级权限 / 选项数据源等能力。

示例:

```sql
COMMENT ON COLUMN users.name     IS '姓名 [@dynamic: fuzzy, exact @sortable @selectable]';
COMMENT ON COLUMN users.password IS '密码 [@hidden]';
COMMENT ON TABLE  users          IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200]';
```

完整示例(users / orders / customers / projects / sys_user / audit_logs,覆盖全指令)见 [dsl.md §8](./dsl.md#8-完整示例) 与 [example/db/schema.sql](./example/db/schema.sql)。

## 查询入参契约(`POST /{resource}/list`)

信封全资源通用,三个数组与 pagination 均可缺省(nil 视为空 / offset 首页)。**入参只接受 camelCase 逻辑键**(见 `fields[].field`),snake_case 物理列名不是合法拼写:

```json
{
  "fields": ["id", "name"],
  "where": [
    { "field": "name", "op": "contains", "value": "al" },
    { "field": "age", "op": "between", "value": 20, "rightValue": 30 }
  ],
  "sort": [{ "field": "createdAt", "order": "desc" }],
  "pagination": { "page": 1, "pageSize": 20, "after": null, "includeTotal": true }
}
```

**JSON 协议面统一小驼峰(lowerCamelCase)**:请求信封、响应信封、`_query-schema` 输出、`@api` 对象的键一律小驼峰;camelCase 逻辑字段名不变。

### 键约束

| 位置 | 键 | 类型 | 约束 |
|---|---|---|---|
| `fields` | 数组 | string | 逻辑字段名;缺省(nil)= 返回全部非 `@hidden` 列;**显式空数组 `[]` 拒绝**(与缺省语义二分);重复去重保序;`@hidden`/select 禁用字段拒绝 |
| `where` | `field` | string | 逻辑字段名(大小写敏感白名单) |
| `where` | `op` | string | 该字段能力集内(见 `_query-schema` 的 `fields[].ops`;操作符语义见 [dsl.md §4.4](./dsl.md#44-操作符语义)) |
| `where` | `value` | any | 主比较值,严格类型收敛(integer 校验整数性、datetime 只收 RFC3339、date 只收 `YYYY-MM-DD`,不收字符串数字);`is_null`/`not_null` **禁止携带** |
| `where` | `rightValue` | any | 仅 `between` 使用且必填(左=value、右=rightValue,含两端) |
| `sort` | `field` | string | 逻辑字段名;仅 `sortable=true` 字段,禁用字段拒绝 |
| `sort` | `order` | string | `asc`/`desc` 大小写不敏感 |
| `pagination` | `page` | int | 从 1 起,≤ 0 拒绝;仅 offset 模式生效 |
| `pagination` | `pageSize` | int | 双模式共用每页条数;显式提供且 ≤ 0、或超 `pagination.maxPageSize` 拒绝;缺省 = `defaultPageSize` |
| `pagination` | `after` | string \| null | **非空即游标模式**(深分页,见下方"游标翻页调用流程");与 `page > 1` 混用 → 400;空串 `""` 非法 |
| `pagination` | `includeTotal` | bool | 是否计算 total;默认 true;false 时响应 `total` 为 null 且不执行 COUNT |

### where 语义

- 多条条件之间恒为 **AND**,无 OR / 条件分组(需要 OR 的场景走 sqlc 静态查询);
- **条件冲突显式拒绝**:任意两个条件翻译后命中同一目标字段(含 `between` 拆分与独立 `gte`/`lte` 重叠、同 field+op 重复)→ 400 `conflicting conditions`,定位到后一条下标;不存在"后写覆盖先写";
- 上限:where 条数 ≤ 20;`in`/`not_in` 单数组 ≤ 1000;单语句占位符总预算 60,000(超限 400)。

### 分页双模式(由请求显式形态决定,无隐式回退)

| 请求形态 | 模式 | 生效参数 |
|---|---|---|
| `after` 缺省或 null | offset | `page`(默认 1)、`pageSize`;归一化后 Offset 超 100,000 → 400(引导改用 `after`) |
| `after` 为非空字符串 | 游标(keyset) | `pageSize` 作为 limit;`page` 必须缺省或 = 1;无累计翻页深度限制 |

游标是**不透明凭证**(base64url + 排序指纹):换排序、跨表复用旧游标 → 400 `fingerprint mismatch`;篡改最坏导致空集或 400。**offset 模式同样返回 `nextCursor`**——客户端首页正常请求,随时可携带它无缝切换游标模式续翻。

#### 游标翻页调用流程(入参解读)

游标模式面向深分页 / 无限滚动:`pagination.after` 填**上一页响应返回的 `nextCursor`**,原样透传即可——不解析、不拼接、不修改。请求与响应均以 JSON 为主:

**第 1 页(offset 模式,不传 `after`)**:

```json
{
  "pagination": { "page": 1, "pageSize": 20 }
}
```

响应(`nextCursor` 为续翻凭证,其余字段省略):

```json
{
  "items": [ { "…": "…" } ],
  "total": 42,
  "page": 1,
  "pageSize": 20,
  "nextCursor": "eyJmIjoiMTIzNDU2Nzg5MCIsInYiOlszMCw1XX0",
  "hasMore": true
}
```

**第 2 页(游标模式)**:把上一页响应的 `nextCursor` 原样填入 `pagination.after`:

```json
{
  "pagination": { "pageSize": 20, "after": "eyJmIjoiMTIzNDU2Nzg5MCIsInYiOlszMCw1XX0" }
}
```

此后每页都取上一页响应中的**新 `nextCursor`** 填入 `after`,直到 `hasMore=false` 为止。

`after` 的值(base64url)解码后是如下 JSON 结构(仅示意):

```json
{
  "f": "1234567890",
  "v": [30, 5]
}
```

`f` 是本资源有效排序的排序指纹(十进制字符串),`v` 是与排序一一对应的排序列值。协议面上 `after` 的类型是 `string \| null`,客户端只透传、不解析、不构造。

调用方需注意的入参约束:

- **`after` 非空即进入游标模式**:此时 `page` 必须缺省或 = 1,`page > 1` 与 `after` 同时出现 → 400;空串 `""` 也是非法游标。
- **`after` 只对本资源 + 本排序有效**:换排序、跨表、跨能力视图复用旧游标 → 400 `fingerprint mismatch`——排序变化由服务端强制校验(指纹含资源名与有效排序),无需客户端自行判断。
- **`where` 也必须与生成游标时保持一致**:排序指纹不含 where,服务端**不会拦截** where 变化——但换过滤条件后,游标定位(排序位置)与新的过滤叠加,结果是"满足新 where 且排序在游标行之后"的行,可能出现跳过行或空页,且**不报错**。因此 **where 或 sort 任一变化,都应丢弃旧 `after` 重新从首页翻**。
- **游标模式下 `pageSize` 即每页条数**(limit);`includeTotal` 照常生效,`total` 语义不变(满足过滤条件的总数,与翻页位置无关)。
- **`nextCursor` 仅 `hasMore=true` 时返回**;offset 模式的首页响应同样携带它,客户端随时可无缝切到游标模式续翻,无需重新发起首页请求。
- **游标是不透明凭证,不含权限信息**:被篡改最坏导致空集或 400,不可能越权读到数据;行级数据权限见 [scope.md](./scope.md)(游标重放同样受权限谓词约束,只收窄不放行)。

**上一页:缓存每页的 `nextCursor` 即可回翻**(游标模式本身单向,无需服务端 `before` 支持)。游标是确定性的——同一 `after` + 同一过滤/排序 → 同一页数据,因此客户端保存已访问各页的 `nextCursor` 序列,回到第 K 页就是把第 K-1 页响应的 `nextCursor` 重新作为 `after` 发出:

```text
cursors = [null, c1, c2, c3, ...]   // cK = 第 K 页响应的 nextCursor
fetchPage(K): after = cursors[K-1]   // K 从 1 起;回到第 K 页 = 重发该 after
```

缓存的是轻量游标字符串(每页几十字节),远优于缓存 `items` 本体;回到旧页后响应仍带新的 `nextCursor` 与 `hasMore`,可继续前进,"上一页 → 下一页"往返都成立。此方案仅在同一过滤 + 排序的视图内有效(改视图后旧游标会 `fingerprint mismatch`,清空缓存重翻首页);翻页期间数据变动时,重发同一 `after` 的页可能轻微漂移——与 keyset 分页对数据变动的固有敏感一致。

### 响应信封

```json
{
  "items": [ { "…": "…" } ],
  "total": 42,
  "page": 1,
  "pageSize": 20,
  "nextCursor": "…",
  "hasMore": true
}
```

| 键 | 说明 |
|---|---|
| `items` | 本页数据(动态列选择:未选中的列在结果中为**零值**,与 NULL/真零值不可区分;handler 可按请求 `fields` 做 DTO 化) |
| `total` | 满足过滤条件的总数(只吃 where,与分页模式/游标位置无关);`includeTotal=false` 时为 null |
| `page` | offset 模式的页码;游标模式为 null |
| `pageSize` | 实际每页条数 |
| `nextCursor` | `hasMore=true` 时的续翻凭证;两模式均返回 |
| `hasMore` | `LIMIT pageSize+1` 探测结果;offset 模式据此也可判断"还有下一页" |

**错误形态**:所有校验失败返回结构化错误 `{ "kind": "where"\|"sort"\|"fields"\|"pagination"\|"cursor", "field": …, "index": …, "reason": … }`(kind=cursor 定位到游标内容,其余定位到数组下标),无静默修正。

**typed 门**(内部调用方):直接构造生成的 `ListUsersParams` 结构体(编译期安全),经 `ListUsers` / `CountUsers` 执行;形态复核(禁用命中、Limit/Offset 上限、After 与 Offset 互斥)与协议门汇合于同一路径,错误为 Go error 而非 400。

## 行级数据权限(@scope)

> 完整机制(OR 组合语义、fail-closed、协议门隔离、调用形态)见 [scope.md](./scope.md);DSL 语法(op 封闭集合、参数类型词表、SQL 形态)见 [dsl.md §5](./dsl.md#5-行级数据权限scope)。

表级指令 `@scope: <列> <op> <函数>(<参数>,...) ...` 为动态表声明行级数据权限:权限规则集中在 DB 守卫函数里只写一次(随迁移版本化),业务代码零权限感知。多守卫恒为 OR 组合;`params.Scope` 为 nil 时 List/Count 直接报错(fail-closed),权限值只能来自服务端注入(`dynq.WithScope`)。example 的 `customers` / `projects` / `sys_user` 即完整演示。

## 前端 Schema(UI 契约)

> 完整契约(schema 键表、前端渲染规则、@api 选项数据源)见 [schema.md](./schema.md)。

每张动态表生成 `UsersQuerySchema(opts ...dynq.QueryOption) dynq.ResourceSchema`,经 `GET /{resource}/_query-schema` 交付,前端据此渲染通用 QueryBuilder——一次开发,全部列表页复用;端点级能力差异(禁用)对前端透明。`ResourceSchema.Hash()` 为内容 hash,可作 ETag 长缓存。

## 目录结构

```text
tools/sqlc_gen_dynamic/     # 插件本体(options / DSL 解析 / Catalog 遍历 / 生成模板)
dynq/                       # 运行时库(手写,信封/收敛/游标/排序/QueryOption/schema)
example/db/                 # 示例工程(schema.sql + queries.sql + sqlc.yaml + main.go + gen/)
bin/                        # 构建产物(已 gitignore)
```
