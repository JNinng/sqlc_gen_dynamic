# sqlc_gen_dynamic

sqlc 自定义 codegen 插件:从 DDL 注释 DSL 生成**动态单表 List 查询代码**(运行期组合 WHERE / ORDER BY / 分页 / 投影),与 sqlc-gen-go 的固定形态查询互补。配套运行时库 `dynq` 与示例工程 `example/`。设计文档见 [desc.md](./desc.md)。

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

# 动态查询演示:启动即重建测试库 → 应用 schema → 种子数据 → 跑 9 组查询演示
# (过滤/冲突拒绝/offset 分页/游标翻页/窄投影/opts 禁用/schema/跨表游标互斥/@scope 行级数据权限)
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
go test ./internal/... ./tools/...
go test ./example/...

# make release-wasm
GOOS=wasip1 GOARCH=wasm go build -o bin/sqlc_gen_dynamic.wasm ./tools/sqlc_gen_dynamic
sha256sum bin/sqlc_gen_dynamic.wasm
```

## 生成产物

`make sqlc` 后:

- `example/db/gen/models.go`、`queries.sql.go`、`db.go` 等:sqlc-gen-go 产物(模型与固定查询)
- `example/db/gen/*_dynamic_gen.go`:本插件产物(registry / 字段常量 / Params / Parse / Schema / List / Count)

## 注释 DSL(生成策略)

生成策略全部声明在 DDL 注释中,随迁移版本化。指令必须包裹在 `[ ]` 中且内容以 `@` 起始;不以 `@` 起始的方括号是 label 字面文本(人类注释不误伤)。列级 / 表级指令:

| 指令 | 级别 | 说明 |
|---|---|---|
| `@dynamic: <项,...>` | 列 | 声明过滤操作符;项可为预设或显式 op,与列类型能力取交集 |
| `@sortable` | 列 | 可参与排序 |
| `@selectable` | 列 | 纯 UI 提示:前端表格列头渲染(服务端 fields 点名白名单 = 全部非 `@hidden` 列) |
| `@api: <endpoint> value_key=<...> label_key=<...> [mode=<single\|multi\|search>]` | 列 | 选项数据源为远程接口(引用字段 ID 选择器,见下方 @api 契约);仅 `integer` / `uuid` / `string` 列 |
| `@hidden` | 列 | 全能力排除:不参与投影/点名/过滤/排序/schema/游标;与上述任一正向标记组合 → 生成期 fail |
| `@default_sort: <列> <ASC\|DESC>` | 表 | 缺省排序回落;目标列必须已声明 `@sortable` |
| `@page: <默认>/<上限>` | 表 | 分页每页条数默认与上限(协议键 `pageSize`) |
| `@pk: <列>` | 表 | 主键列(tiebreaker),含动态能力的表必填;指向可空列或 `@hidden` 列 → 生成期 fail |
| `@scope: <列> <op> <函数>(<参数>,...) ...` | 表 | 行级数据权限:多守卫 OR 组合。op ∈ `in`(集合守卫)/`eq`/`gt`/`gte`/`lt`/`lte`(标量守卫);参数为标识符 `name:type`(类型词表 `varchar`/`int`→int64/`bool`/`varchar[]`/`int[]`/`jsonb`,值来自生成的 Scope 结构体)或字面量(`'字符串'`/整数,内联);目标列须存在、可与 `@hidden` 组合,一列一守卫;见下方[行级数据权限](#行级数据权限scope) |

预设展开:

| 预设 | 展开为 op |
|---|---|
| `exact` | `eq`, `neq` |
| `in` | `in`, `not_in` |
| `range` | `gt`, `gte`, `lt`, `lte`, `between` |
| `fuzzy` | `contains`, `prefix`, `suffix` |
| `has` | `has` |

`is_null` / `not_null` 刻意不设预设:空值判断必须逐列显式声明,且仅可空列可声明。

操作符语义(UI 推断表中"含 xx op"即指此):

| op | SQL 语义 | 适用 dataType |
|---|---|---|
| `eq` / `neq` | `=` / `<>` | integer, number, datetime, date, string, boolean, uuid, enum |
| `gt` / `gte` / `lt` / `lte` | 对应比较运算符 | integer, number, datetime, date |
| `between` | `>= AND <=`(含两端,value=左端、rightValue=右端) | integer, number, datetime, date |
| `in` / `not_in` | `IN` / `NOT IN`(展开占位符,1 ≤ len ≤ MaxIn) | 除 jsonb 外全部 |
| `contains` / `prefix` / `suffix` | `ILIKE '%v%'` / `'v%'` / `'%v'`(value 中 `%` `_` `\` 全量转义,通配符位置由 op 决定) | string |
| `is_null` / `not_null` | `IS NULL` / `IS NOT NULL`(禁止携带 value) | 任意,仅可空列且显式声明 |
| `has` | `@>`(json 包含) | jsonb |
| `in`(int_array 列) | `&&`(数组重叠) | int_array |

示例(见 `example/db/schema.sql`):

```sql
COMMENT ON COLUMN users.name       IS '姓名 [@dynamic: fuzzy, exact @sortable @selectable]';
COMMENT ON COLUMN users.password   IS '密码 [@hidden]';
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: is_null, not_null]';
COMMENT ON TABLE  users            IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200]';

COMMENT ON TABLE  customers        IS '客户表 [@scope: owner_user_id in fn_accessible_user_ids(user_id:varchar, scopes:int[]) owner_dept_id in fn_accessible_dept_ids(user_id:varchar, scopes:int[]) id in fn_permitted(''customers'', user_id:varchar) @pk: id]';
```

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
| `fields` | 数组 | string | 逻辑字段名;缺省(nil)= 默认全量投影;**显式空数组 `[]` 拒绝**(与缺省语义二分);重复去重保序;`@hidden`/select 禁用字段拒绝 |
| `where` | `field` | string | 逻辑字段名(大小写敏感白名单) |
| `where` | `op` | string | 该字段能力集内(见 `_query-schema` 的 `fields[].ops`;操作符语义见上文 DSL 表) |
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
- **游标是不透明凭证,不含权限信息**:被篡改最坏导致空集或 400,不可能越权读到数据;行级数据权限见下方 [@scope 机制](#行级数据权限scope)(游标重放同样受权限谓词约束,只收窄不放行)。

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
| `items` | 本页数据(动态投影:未查询的列在结果中为**零值**,与 NULL/真零值不可区分;handler 可按请求 `fields` 做 DTO 化) |
| `total` | 满足过滤条件的总数(只吃 where,与分页模式/游标位置无关);`includeTotal=false` 时为 null |
| `page` | offset 模式的页码;游标模式为 null |
| `pageSize` | 实际每页条数 |
| `nextCursor` | `hasMore=true` 时的续翻凭证;两模式均返回 |
| `hasMore` | `LIMIT pageSize+1` 探测结果;offset 模式据此也可判断"还有下一页" |

**错误形态**:所有校验失败返回结构化错误 `{ "kind": "where"\|"sort"\|"fields"\|"pagination"\|"cursor", "field": …, "index": …, "reason": … }`(kind=cursor 定位到游标内容,其余定位到数组下标),无静默修正。

**typed 门**(内部调用方):直接构造生成的 `ListUsersParams` 结构体(编译期安全),经 `ListUsers` / `CountUsers` 执行;形态复核(禁用命中、Limit/Offset 上限、After 与 Offset 互斥)与协议门汇合于同一路径,错误为 Go error 而非 400。

## 行级数据权限(@scope)

表级指令 `@scope: <列> <op> <函数>(<参数>,...) ...` 为动态表声明行级数据权限:权限规则集中在 DB 守卫函数里只写一次(显式参数、可单独 `SELECT * FROM fn(...)` 测试、定义在 sqlc 可见的 schema 内随迁移版本化),业务代码零权限感知。example 的 `customers`(三条 OR 守卫:归属用户集合 / 归属部门集合 / 显式共享字面量参数)与 `projects`(eq 租户标量守卫)即完整演示。

### 机制

- **DSL 声明**:多条守卫恒为 OR 组合(任一命中即可见);op 复用业务操作符语义矩阵,SQL 语义同源:

  | op | SQL 形态 | fn 返回 |
  |---|---|---|
  | `in` | `col IN (SELECT * FROM fn(...))` | 单列集合,类型与目标列一致 |
  | `eq` | `col = fn(...)` | 标量;**NULL 列对比较恒为假(不可见,fail-closed 方向)** |
  | `gt`/`gte`/`lt`/`lte` | `col > fn(...)` 等(时间窗权限) | 标量 |

  刻意排除 `neq`/`not_in`——`NOT IN` 撞上集合含 NULL 时恒空,是权限事故配方。
- **参数**:标识符参数 `name:type`(类型词表 `varchar`→string、`int`→**int64**、`bool`、`varchar[]`→[]string、`int[]`→[]int64、`jsonb`→json.RawMessage,词表外类型生成期 fail;字段汇入该表的 `XxxScope` 结构体,多守卫同名参数去重合并、类型冲突 fail)或字面量参数(`'字符串'`/整数,校验后内联进 SQL);允许零参函数。
- **守卫函数契约**:函数本体不在 sqlc catalog,不做存在性校验(与 `@api` endpoint 同一信任级别),由部署侧保证;函数名须为小写标识符(可带一层 schema 限定,会拼进 SQL 字面量故严格校验)。
- **谓词注入**:`buildXxxWhere` 头部按声明内联生成守卫谓词(零反射,编译期类型检查),与业务条件 AND;IN-子查询与不相关标量函数在单条语句内只求值一次;List 与 Count 共用 → **total 与 items 的权限视图天然一致**。
- **fail-closed**:声明 `@scope` 的表,`params.Scope` 为 nil 时 List/Count 直接报错(`Kind=scope`),宁可拒绝服务也不静默退化为全量可见。
- **协议门结构性隔离**:客户端 JSON 不存在设置 Scope 的通道,权限值只能来自服务端注入。
- **硬边界**:一列一守卫(重复声明 fail);参数不做行相关(不允许引用本行其他列——相关子查询退化为逐行求值);需要 AND 复合逻辑(租户 AND 归属)时写进函数内部或交给 RLS 层,DSL 保持扁平 OR。

### 调用形态

```go
// middleware:认证解析后一次注入(Scope 类型按资源生成,字段与守卫参数一一对应)
ctx = dynq.WithScope(ctx, &gen.CustomersScope{UserID: "u4", Scopes: []int64{2, 3}})

// handler:业务参数照常解析,权限一行补齐
params, err := gen.ParseCustomersQuery(q)
params.Scope = dynq.ScopeFromContext[gen.CustomersScope](ctx)
items, err := gen.ListCustomers(ctx, db, params)
total, err := gen.CountCustomers(ctx, db, params) // 同一 params → 同一权限视图
```

`Scope` 是 Params 的保留字段,与业务槽位"零值=条件不参与"不同——它为 nil 是显式错误。守卫谓词不占协议门的条件冲突槽位,与业务条件正交叠加(如权限视图内再按 `name contains` 收窄);`scopes int[]` 一类数组参数天然承载多角色并集语义(函数内 `= ANY`)。游标重放受权限谓词约束:权限收窄后旧游标最坏跳行/空页,不可能越权读到数据。

## 前端 Schema(UI 契约)

每张动态表生成 `UsersQuerySchema(opts ...dynq.QueryOption) dynq.ResourceSchema`,前端据此渲染通用 QueryBuilder——一次开发,全部列表页复用;端点级能力差异(禁用)对前端透明。

- **交付端点**:`GET /{resource}/_query-schema`;`ResourceSchema.Hash()` 为内容 hash,可作 ETag 长缓存(同一 opts 切片产出字节级相同 schema)
- **减法投影**:与 Parse 完全相同的 opts 语义——where 禁用 → 该字段 `ops` 清空;sort 禁用 → `sortable=false`;select 禁用 → `selectable=false`;字段被完全禁用(或原 `@hidden`)→ 从 schema 消失,QueryBuilder 无需感知"禁用"概念

### 资源级键(ResourceSchema)

| 键 | 值类型 | 含义 |
|---|---|---|
| `resource` | string | 资源名(如 `"users"`) |
| `fields` | FieldSchema[] | 字段契约列表,见下表 |
| `defaultSort` | `{ "field": string, "desc": boolean }[]` | 缺省排序回落(查询未指定排序时生效) |
| `pagination` | object | 见下方 pagination 键表 |

### 字段契约键(FieldSchema)

| 键 | 值类型 | 含义 |
|---|---|---|
| `field` | string | 逻辑字段名(camelCase) |
| `label` | string | 展示文本(注释非指令部分) |
| `dataType` | string(枚举) | `integer` / `number` / `datetime` / `date` / `string` / `boolean` / `uuid` / `enum` / `jsonb` / `int_array` |
| `nullable` | boolean | 列可空性 |
| `sortable` | boolean | 可排序 → 表格列头可挂排序 |
| `selectable` | boolean | 纯 UI 提示:列头渲染与显隐交互(见 `@selectable`) |
| `ops` | string[] | 本调用点有效的 where 操作符全集(能力上限 = 生成期 DSL;**操作符渲染的唯一事实源**,见下方渲染规则) |
| `enum` | string[](可选) | 枚举值列表(仅 `enum` 类型,下拉组件的 options 取自此;编译期可确定时用,静态) |
| `api` | object(可选) | 选项数据源为远程接口(`@api` 声明);有 `api` 时值控件 options 经接口拉取,`enum` 不出现。见下方 @api 契约 |

> schema **不携带 `ui` 字段**:控件族由 `dataType` 唯一确定(映射表见下方渲染规则),操作符与值控件形态由 `ops` 驱动——渲染信息全部可由 `(dataType, ops, sortable, selectable)` 派生,不做第二份副本。

### pagination 键

| 键 | 值类型 | 含义 |
|---|---|---|
| `defaultPageSize` | number | pageSize 缺省值(表级 `@page` 可覆盖) |
| `maxPageSize` | number | pageSize 上限 |
| `modes` | string[] | 分页模式子集:`["offset","cursor"]` |
| `includeTotal` | boolean | 是否支持 `includeTotal=false`(total 为 null,前端隐藏"共 N 条") |

### 前端渲染规则(契约,固化于 §10.2)

前端 QueryBuilder 依据 `(dataType, ops, sortable, selectable)` 渲染,控件族由 `dataType` 唯一确定:

- **操作符下拉**:状态 = 同层 `ops` 中除 `is_null` / `not_null` 的全部 op(该两类为 API-only,不渲染)。字段仅 1 个 op 时不渲染下拉。
- **值控件形态由所选 op 决定**:`between` → 双控件(区间);`in` / `not_in` → 多值输入;其余 → 单值输入。
- 后端不过滤、不复制 ops —— 顶层 `ops` 是操作符渲染的唯一事实源,不存在"渲染漏掉顶层 op"的口子。

| dataType | 控件族 | 说明 |
|---|---|---|
| string / uuid | 输入框 | uuid 前端格式预校验由 `dataType` 提示 |
| integer / number | 数字输入 | |
| datetime / date | 时间 / 日期选择 | |
| enum | 下拉选择 | 单选 / 多选形态由所选 op 决定,options = `fields[].enum` |
| boolean | 布尔选择 | |
| int_array | 标签输入 | |
| jsonb / 仅 `is_null`+`not_null` / where 域整体禁用 | 不渲染 | 留给 API 直调方 |

分页组件按 `pagination.modes` 渲染:含 `offset` → 页码跳转;含 `cursor` → 无限滚动;两者皆含 → 混合分页。`includeTotal=false` 时 total 为 null,前端隐藏"共 N 条"。

### @api 契约(引用字段选项数据源)

`@api` 服务于**引用型字段的 ID 选择器**(如 user_id / assignee_id,值为动态业务数据):该字段 where 值控件的 options 不从静态 `enum` 取,而是经 `GET/POST {endpoint}` 拉取。schema 输出字段级 `api` 对象:

```json
{
  "field": "userId",
  "dataType": "integer",
  "api": {
    "endpoint": "/api/v1/users/options",
    "valueKey": "id",
    "labelKey": "name",
    "mode": "multi"
  }
}
```

契约要点:

- **`endpoint`** 相对前端 baseURL;`valueKey` / `labelKey` 为响应 `items` 每项的**顶层字段名**,值类型与字段 `dataType` 一致。
- **`mode`** 为最终交互模式:显式声明优先;否则由 ops 推导——含 `in` → `multi`、含 `contains`/`prefix`/`suffix` → `search`、否则 `single`。**任意 mode 均支持 `page/pageSize` 翻页**(分页是选项接口的通用能力,与 mode 无关);`search` 模式前端额外携带 `q` 关键字。
- **响应信封**(全资源统一):`{"items": [{"<valueKey>": ..., "<labelKey>": ...}], "hasMore": false}`;`hasMore=true` 时前端携带 `page+1` 继续拉取。两种实现均可:
  - 复用生成方法:`ListUsers(ctx, db, ListUsersParams{NameContains: q, Limit: pageSize})` → `items` 即 `Result.Items`(每行模型,字段名天然匹配 `valueKey`/`labelKey`);
  - 独立 options 接口(推荐生产使用:脱敏、限字段、缓存)。
- `@api` 仅影响前端 schema 输出,不参与任何服务端校验路径;与 `@hidden` 组合、非 `integer`/`uuid`/`string` 列、缺 `value_key`/`label_key`(DSL 参数保持 snake,JSON 输出为小驼峰)、未知参数、endpoint 含 `?` → 生成期 fail。

### 示例(`UsersQuerySchema()` 节选)

```json
{
  "resource": "users",
  "fields": [
    {
      "field": "name",
      "label": "姓名",
      "dataType": "string",
      "nullable": false,
      "sortable": true,
      "selectable": true,
      "ops": ["contains", "prefix", "suffix", "eq", "neq"]
    },
    {
      "field": "status",
      "label": "状态",
      "dataType": "enum",
      "sortable": true,
      "ops": ["eq", "neq", "in", "not_in"],
      "enum": ["active", "inactive", "banned"]
    },
    {
      "field": "deletedAt",
      "label": "删除时间",
      "dataType": "datetime",
      "nullable": true,
      "ops": ["is_null", "not_null"]
    }
  ],
  "defaultSort": [{ "field": "createdAt", "desc": true }],
  "pagination": { "defaultPageSize": 20, "maxPageSize": 200, "modes": ["offset", "cursor"], "includeTotal": true }
}
```

## 目录结构

```text
tools/sqlc_gen_dynamic/     # 插件本体(options / DSL 解析 / Catalog 遍历 / 生成模板)
dynq/                     # 运行时库(手写,信封/收敛/游标/排序/QueryOption/schema)
example/db/                 # 示例工程(schema.sql + queries.sql + sqlc.yaml + gen/)
bin/                        # 构建产物(已 gitignore)
```
