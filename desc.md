# sqlc_gen_dynamic 动态查询插件设计文档

|      |                                                  |
|------|--------------------------------------------------|
| 状态   | 已实施                                                |
| 日期   | 2026-08-22                                       |
| 关联产物 | 插件 `sqlc_gen_dynamic`、运行时库 `dynq`、生成物包 `dynamic` |

---

## 1. 背景与选型

### 1.1 问题

项目使用 sqlc 生成数据库访问代码。sqlc 的核心约束是**查询形态编译期固定**，而列表页需要运行期组合的 WHERE / ORDER BY /
分页 / 投影。纯静态方案（`NOT @has_x OR x=@x` 模式）在字段增多时 SQL 与参数结构体组合爆炸；引入 ORM 与现有 pgx 体系双轨冲突。动态
List 是 sqlc 社区常年 Top 需求，短期无法指望官方支持。

### 1.2 选型结论

| 方案                                  | 结论                   |
|-------------------------------------|----------------------|
| A. sqlc 纯静态 + 可选参数模式                | 仅适用于 ≤3 个固定组合字段，不可扩展 |
| B. sqlc + squirrel 手写动态查询           | 可行但手工重复，是本设计要消灭的现状   |
| C. 独立 codegen（读 information_schema） | 可行备选，与 E 产物形态同构      |
| D. 引入 ORM                           | 排除（连接池双轨、反射开销、黑盒）    |
| **E. sqlc 自定义插件**                   | **选定**               |

选 E 的理由：团队已重度投资 sqlc；`sqlc generate` 一条命令覆盖全部生成物；CI 零数据库依赖；查询能力声明可写入 DDL 注释随迁移版本化。E
的产物形态与 C 完全同构（业务层只见接口），未来切换生成器上层代码不动。

### 1.3 总体分工

- **固定形态查询**（GetBy / Create / Update / Delete / upsert / JOIN / CTE / 聚合）→ sqlc-gen-go 静态定义
- **动态单表 List**（前端过滤、排序、分页、动态投影）→ 本插件生成
- **SQL 拼装** → squirrel，但使用收敛在生成物内部
- **连接与事务** → 完全由 pgx 承担，生成代码经 DBTX 接口注入，自身无状态

---

## 2. 目标与非目标

**目标**

1. 动态 List 的字段、操作符、排序、分页、投影全部由生成期元数据驱动，业务层零手写。
2. 同时服务两类调用方：HTTP 协议入口（JSON）与内部调用方（typed 结构体）。
3. 为前端输出可反推查询组件 UI 的 schema，与后端校验规则同源。
4. 生成物提交入库，schema 漂移在编译期 / CI 暴露。
5. **分页双模式**：offset（浅翻页、跳页）与 keyset 游标（深翻页、无限滚动）共用同一查询管线；游标对调用方不透明且自带排序指纹；total
   计算成本可由调用方按需关闭。
6. **动态投影**：支持前端按需指定查询列或排除敏感列；列名受白名单严格校验，防止越权读取。
7. **运行期能力收窄**：调用点可用选项按端点 / 角色禁用部分 where、sort、select 字段；禁用幂等地反映到协议校验、查询执行与前端
   schema 三处，所见即所查。
8. **全部校验显式拒绝**：任何冲突、越权、形态错误的输入都返回明确定位的错误，不存在静默覆盖、静默修正、静默 no-op 路径。
   **非目标**

- 跨表 JOIN、CTE、窗口函数的动态化（走 sqlc 静态查询或 Postgres VIEW 包装）。
- 写路径（增删改）的动态化。
- **条件分组（OR / 嵌套组合）**：where 数组恒为 AND 语义，不支持 OR 与括号分组。需要 OR 的场景走 sqlc 静态查询。此为协议级约束，前端
  QueryBuilder 不提供分组 UI。
- 反向游标（`before`）与游标化随机跳页——跳页仍由 offset 承担并受上限约束。
- 通过选项**扩展**生成期未声明的能力（选项只做减法）。
- **查询层无业务副作用**：不在生成的查询接口中提供 Pre/Post Hook（如查询前加解密、查询后脱敏）。查询层只负责忠实将 `Params`
  翻译为 SQL 并扫描结果。数据转换职责应通过 DBTX 自定义 Scanner 或业务层包装实现。

---

## 3. 总体架构

### 3.1 双门模型

```
HTTP 入口                                      内部调用方（定时任务、服务间）
  │                                                 │
  ▼                                                 ▼
dynq.DecodeStrict ──▶ ParseUsersQuery(q, opts...)   ListUsersParams{...}（直接构造）
                          │                              │
                          └──▶ ListUsersParams ──────────┤  opts 在此防御性复核
                                   │                      │
                                   ▼                      ▼
                         ListUsers(ctx, db, params, opts...)
                                   │
              ① opts → QueryConfig（registry 校验禁用目标）
              ② validateUsersParams（形态复核，List/Count 共享）
              ③ 有效排序解析（用户排序 / @default_sort / tiebreaker / NULLS 方位）
              ④ 游标解码 + 指纹比对 + 值类型收敛（双门唯一路径）
              ⑤ buildUsersSelect（投影 = 请求字段 ∪ 有效排序列）
              ⑥ buildUsersWhere（过滤，List / Count 共享）
              ⑦ buildUsersSeek（游标定位，仅 List 追加）
                                   │
                         UsersQuerySchema(opts...) ──▶ GET /users/_query-schema
                                   │
                         squirrel（sq.Dollar）→ pgx（DBTX 注入）
```

- **协议门**：JSON 信封 → 严格解码 → 生成的解析函数（运行期白名单 = 生成期白名单 − 运行期禁用）→ typed Params。游标在此门仅做
  **形态判定**（模式选择、互斥检查），内容解码统一下沉到查询函数。
- **typed 门**：内部调用方直接构造 Params，编译期安全；opts 校验与游标校验在此门兜底，与协议门汇合于同一代码路径。
- 两扇门汇合于同一 Params / 查询函数 / 同一 opts，引擎只有一份。
- **同参纪律**：同一 opts 切片必须贯穿该端点的 `ParseUsersQuery`、`ListUsers` / `CountUsers`、`UsersQuerySchema`
  全部调用点，保证“协议可发什么、引擎会执行什么、前端能渲染什么”三者恒等。

### 3.2 核心不变量

1. **三投影同源**：协议校验白名单、typed Params、前端 schema 由同一份生成期元数据在同一模板循环中产出。同源性由构造保证，物理上不可能漂移。
2. **生成代码为纯函数**：签名统一为 `(ctx, DBTX, params, opts...)`，不持有连接状态；无副作用。
3. **squirrel 边界**：仅出现在生成物内部，业务层不直接 import；所有 builder 使用 `sq.Dollar` 占位符格式。
4. **物理列名不外泄**：所有对外输入与输出（协议字段名、Params 字段、协议错误、前端
   schema、游标）只使用逻辑字段名（camelCase）与列值。snake_case 物理列名不是任何输入面的合法拼写。
5. **过滤 WHERE 单一事实源**：List 与 Count 共享同一过滤 WHERE 构造函数；游标 seek 谓词与 LIMIT 仅作用于 List。total 语义 =
   满足过滤条件的总数，与分页模式、游标位置无关。
6. **投影并集不变量**：实际 SELECT 列集 ⊇ 有效排序列集（含 tiebreaker）。排序列无条件强制并入投影，保证游标构造与 hasMore
   探测在任何 `fields` 收窄下都成立。
7. **游标校验单点**：游标 base64/JSON 解码、排序指纹比对、列值类型收敛只存在于查询函数内部（两扇门共用同一路径），协议解析函数仅透传
   `after` 字符串并做形态判定。
8. **运行期能力投影**：`QueryOption` 是对生成期白名单的**纯减法投影**
   ——只能禁用、不能开启；禁用为集合语义（幂等、可交换、去重）；有效能力 = 生成期白名单 − 禁用集，四个消费点同参。生成期 DSL
   永远是能力上限的唯一事实源。
9. **零静默路径**：条件冲突、禁用命中、形态错误一律显式拒绝；不存在“后写覆盖先写”“静默剔除”“静默回落”。
10. **生成物入库**，CI 锁定与 DDL 的漂移；schema 变更必然出现在 diff 中接受评审。

---

## 4. 插件形态与宿主集成

### 4.1 独立插件 vs fork sqlc-gen-go

选用独立进程/wasm 插件，约数百行自有代码，仅依赖 sqlc-dev/plugin 的 proto 包。当 sqlc.yaml 中自定义类型 override
数量失控时再考虑迁移至 fork 模式；两者生成物对业务层同构，迁移非单向门。

### 4.2 双构建模式

同一份源码，build tag 拆分入口，协议相同（stdin 收 `CodeGenRequest` protobuf，stdout 回 `CodeGenResponse`）：

- **process 模式**（开发期）：`go build` 出本地二进制挂到 sqlc.yaml，改动即生效，可 dlv 调试。
- **wasm 模式**（发布期）：编译为 wasm，sqlc.yaml 以 `url + sha256` 锁定，保证跨机器一致性。

### 4.3 sqlc.yaml 接线

一个 `sql` 块只挂一个 codegen，因此配置两个块**共享同一份 schema.sql**（迁移文件即 DDL 单一事实源）：

- 块 1：sqlc-gen-go，产出模型与固定查询（`sql_package: pgx/v5`）。
- 块 2：本插件，`queries` 字段 sqlc 要求必填但插件忽略，仅消费 Catalog。

### 4.4 插件 options

| option                           | 说明                                       |
|----------------------------------|------------------------------------------|
| `package`                        | 产物包名                                     |
| `model_import` / `model_package` | 块 1 产物位置（复用其模型类型与 DBTX）                  |
| `schema`                         | 目标 schema，默认 `public`                    |
| `tables`                         | 生成范围；缺省为该 schema 下所有含 `@dynamic` 声明的表    |
| `default_ops`                    | 未标注列的策略，默认 `none`（显式声明才生成，防 Params 字段爆炸） |
| `go_type_overrides`              | 逃生舱，镜像 sqlc-gen-go 的类型覆盖                 |
| `runtime_import`                 | dynq 运行时包导入路径                            |
| `page_default` / `page_max`      | 分页全局默认（表注释可覆盖）                           |

### 4.5 版本耦合

插件 proto 与 sqlc 二进制存在版本耦合。升级顺序固定：先升 `sqlc-dev/plugin` 依赖 → 再升 sqlc 二进制 → 全量再生成比对
diff。CI 中 sqlc 版本固定。

### 4.6 dump-first 原则

编写生成逻辑前，先用最小插件把 `CodeGenRequest` dump 为 JSON，验证三件事：

1. `COMMENT ON COLUMN` 文本是否进入 `columns[].comment`。
2. timestamptz / uuid / jsonb / enum 在类型 oneof 中的真实形态。
3. `Schema.Enums` 是否携带完整枚举值列表。
   任一不成立时的退路：列注释 → 表级 JSON 注释或 options 策略表；enum → options 手填。

---

## 5. 元数据与声明 DSL

### 5.1 元数据来源与单一事实源

使用 sqlc 解析 DDL 得到的 Catalog（`CodeGenRequest.Catalog`），而非 information_schema。

- **事实源纪律**：元数据的唯一载体是项目版本控制的 `schema.sql`（迁移文件）。生成物基于此产出，与运行时生产库解耦。
- **DBA 手动修改的影响**：若 DBA 在生产库直接增删改注释，不影响已部署的二进制；但会导致 `schema.sql` 与生产库不一致。该漂移由
  `sqlc vet` 或 CI 的 diff 检查兜底拦截。
- 收益：CI 零数据库依赖、`sqlc generate` 单命令触发。

### 5.2 DSL 语法与边界隔离

为消除人类自然语言注释与机器解析指令的歧义，DSL 引入**边界分隔符**，指令必须包裹在 `[ ]` 中且内容以 `@` 起始。
**列级**：`<label> [@dynamic: <项,项,...>] [@sortable] [@hidden] [@selectable] [@api: <endpoint> value_key=<顶层字段名> label_key=<顶层字段名> [mode=<single|multi|search>]]`
**表级**：`[@default_sort: <字段> <ASC|DESC>] [@page: <默认>/<上限>] [@scope: <列> <op> <函数>(<参数:类型|字面量>,...) ...]`

```sql
COMMENT ON COLUMN users.id         IS 'ID [@dynamic: exact, in @sortable @selectable]';
COMMENT ON COLUMN users.name       IS '姓名 [@dynamic: fuzzy @sortable @selectable]';
COMMENT ON COLUMN users.email      IS '用户邮箱，如 test@example.com [@dynamic: exact, prefix @selectable]';
COMMENT ON COLUMN users.password   IS '密码 [@hidden]';
COMMENT ON COLUMN users.status     IS '状态 [已废弃] [@dynamic: exact, in @sortable]';
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: range, is_null, not_null]';
COMMENT ON COLUMN orders.user_id   IS '用户ID [@dynamic: eq, in @selectable @api: /api/v1/users/options value_key=id label_key=name]';
COMMENT ON TABLE  users            IS '用户表 [@default_sort: created_at DESC @page: 20/200]';
COMMENT ON TABLE  customers        IS '客户表 [@scope: owner_user_id in fn_accessible_user_ids(user_id:varchar, scopes:int[]) owner_dept_id in fn_accessible_dept_ids(user_id:varchar, scopes:int[]) id in fn_permitted(''customers'', user_id:varchar) @pk: id]';
```

**`@api`（引用字段选项数据源）**：服务于**引用型 ID 选择器**（user_id / assignee_id 等，值为动态业务数据）——该字段
where 值控件的 options 经 `GET {endpoint}` 拉取，而非静态 `enum`。约束：

- 仅 `integer` / `uuid` / `string` 列可声明；DSL 参数 `value_key` / `label_key` **必填**（JSON 输出键为小驼峰
  `valueKey` / `labelKey`，即响应 `items` 每项的顶层字段名，值类型与字段 dataType 一致），`mode` 可选
  （`single` / `multi` / `search`，缺省由 ops 推导：含 `in` → `multi`、含 fuzzy 三 op → `search`、否则 `single`）；
- endpoint 为相对前端 baseURL 的路径，**禁止 `?` 查询串**（参数由前端按协议拼）；
- 未知 `key=value` 参数 / `mode` 非法 / 三必填缺失 / 与 `@hidden` 组合 / 类型不支持 → **生成期 fail**；
- `@api` 是纯前端契约，不参与任何服务端校验路径（ID 值仍走原有类型收敛与参数化）。

### 5.3 解析规则与鲁棒性

1. **文本分割**：扫描注释字符串，配对 `[` … `]`。出现未闭合的 `[` → **生成期 fail**，定位到列。
2. **指令块判定**：块内容去首尾空格后**以 `@` 起始** → 指令块，进入指令解析；**不以 `@` 起始** → 视为 label
   字面文本，方括号原样保留（如上例“状态 [已废弃]”）。人类注释中的方括号因此不会误伤。
3. **Label 纯洁性**：非指令块文本（含其中出现的任何 `@` 符号与方括号）原样保留为展示文本，不触发任何解析逻辑。
4. **多指令块**：同一列允许多个指令块，按出现顺序合并解析；同名指令重复声明等价于声明一次（去重）。
5. **指令识别**：合法列级指令为 `@dynamic` / `@sortable` / `@hidden` / `@selectable` / `@api`；合法表级指令为 `@default_sort` /
   `@page` / `@pk` / `@scope`。`[ ]` 内出现白名单外的 `@xxx`（如手误 `@sortble`）→ **生成期 fail**，报错定位到具体列与未知标签名，杜绝静默忽略导致的能力丢失。
6. **无 `[ ]` 或无指令块的纯文本注释**：纯业务说明，直接作为 label，跳过解析。
7. **无 `@dynamic` 的列**不参与过滤能力生成（受 `default_ops` 控制）。
8. **`is_null` / `not_null` 完全独立**：二者互不捆绑、不入任何预设；列可空不会自动获得它们，必须逐个显式声明；声明于非空列 →
   **生成期 fail**。
9. DSL 请求了类型不支持的 op（如对 bool 声明 range）→ **生成期 fail**。
10. **投影控制三标签语义（真值表）**：
    | 列标记组合 | 默认投影 | `fields` 可点名 | UI 列头渲染 | where 过滤 | sort 排序 |
    |---|---|---|---|---|---|
    | 无标记 | ✓ | ✓ | ✗（默认不渲染列头） | ✗ | ✗ |
    | `@selectable` | ✓ | ✓ | ✓（可隐藏/显示） | ✗ | ✗ |
    | `@dynamic` | ✓ | ✓ | ✗ | ✓（声明 op 集） | ✗ |
    | `@sortable` | ✓ | ✓ | ✗ | ✗ | ✓ |
    | 正向标记任意组合 | ✓ | ✓ | 有 `@selectable` 才渲染 | 有 `@dynamic` 才可 | 有 `@sortable` 才可 |
    | `@hidden` | ✗ 永不 | ✗ | 不出现在 schema | ✗ | ✗ |
    | `@hidden` 与任一正向标记组合 | **生成期 fail** | | | | |
    - `@hidden` 是**全能力排除**标记：该列不参与动态层的任何能力（投影、点名、过滤、排序、schema 输出），仅经 sqlc 静态查询访问。
      `@hidden` 与 `@dynamic` / `@sortable` / `@selectable` 组合声明 → 生成期 fail。
    - `@selectable` 是**纯 UI 提示**：服务端 `fields` 点名白名单 = 全部非 `@hidden` 列；`@selectable` 不参与服务端校验，仅流入前端
      schema 决定列头渲染与显隐交互。
11. **表级指令校验**：`@default_sort` 的目标列必须存在、已声明 `@sortable` 且非 `@hidden`，方向必填，否则生成期 fail；`@page`
    的默认值必须 ≤ 上限；`@scope` 见下方专节。

**`@scope`（行级数据权限）**：表级微型语法 `<列> <op> <函数>(<参数,...>)`，多守卫恒为 OR 组合。权限规则集中在 DB
守卫函数里只写一次，函数定义在 sqlc 可见的 schema 内随迁移版本化。要点：

- **op 封闭集合** `in|in_any|eq|gt|gte|lt|lte`：`in` 为集合守卫（`col IN (SELECT * FROM fn(...))`，fn 单列返回、类型与目标列
  一致）；`in_any` 为数组三态守卫（`(fn(...) IS NULL OR col = ANY(fn(...)))`，fn 返回**数组**而非集合——集合形态下"空集"与
  "NULL 行"不可区分（`fn() IS NULL` 二者同为真），三态无法表达；数组形态 NULL（数组本身）与 `{}` 可区分：NULL=放行全部、
  `{}`=拒绝全部、非空=ANY 精确匹配，服务于"NULL 表示全租户"一类显式放行契约，注意其 NULL 放行方向与 `in` 的 fail-closed
  相反，守卫函数须保证 NULL 只由显式分支返回）；其余为标量守卫（`col = fn(...)` 等，NULL 列对比较恒为假——不可见，fail-closed
  方向）。刻意排除 `neq`/`not_in`（`NOT IN` 撞集合含 NULL 恒空，权限事故配方）。
- **参数**：标识符参数 `name:type`（类型词表 varchar→string、int→int64、bool、varchar[]→[]string、int[]→[]int64、
  jsonb→json.RawMessage，词表外生成期 fail）或字面量参数（`'字符串'`/整数，经分词器校验后内联）；允许零参。
  函数名须为小写标识符（可带一层 schema 限定）——拼进 SQL 字面量故严格校验，杜绝注入面。
- **生成形态**：标识符参数并集生成 per-table `XxxScope` 结构体（同名参数类型冲突、不同参数名折叠同一 Go 字段名
  均生成期 fail）；`buildXxxWhere` 头部按声明内联穷举守卫谓词（零反射、编译期类型检查），与业务条件 AND，
  List/Count 共用 → total 与 items 权限视图天然一致；IN-子查询与不相关标量函数单语句内只求值一次
  （`in_any` 谓词 fn 出现两次——IS NULL 判别与 ANY 匹配，STABLE 下结果一致，占位参数随调用双份）。
- **注入与 fail-closed**：`params.Scope` 为保留字段（`*XxxScope`），协议门结构性无法设置；服务端经
  `dynq.WithScope(ctx, &XxxScope{...})` + `dynq.ScopeFromContext[XxxScope](ctx)` 注入；缺省 Scope 时
  List/Count fail-closed 报错（`Kind=scope`），不静默退化为全量可见。
- **硬边界**：一列一守卫（重复声明 fail）；参数不做行相关（相关子查询退化为逐行求值）；AND 复合逻辑
  （租户 AND 归属）写进函数内部或交给 RLS，DSL 保持扁平 OR。

### 5.4 预设展开

| 预设      | 展开为 op                    |
|---------|---------------------------|
| `exact` | eq, neq                   |
| `in`    | in, not_in                |
| `range` | gt, gte, lt, lte, between |
| `fuzzy` | contains, prefix, suffix  |
| `has`   | has                       |

预设名 `fuzzy` 与 op `contains` 显式区分，消除同名歧义。`is_null` / `not_null` 刻意**不设预设**：空值判断必须逐列显式声明。

---

## 6. 查询协议规范

### 6.1 信封结构

```json
{
  "fields": [
    "id",
    "name"
  ],
  "where": [
    {
      "field": "",
      "op": "",
      "value": null,
      "rightValue": null
    }
  ],
  "sort": [
    {
      "field": "",
      "order": ""
    }
  ],
  "pagination": {
    "page": 1,
    "pageSize": 20,
    "after": null,
    "includeTotal": true
  }
}
```

| 位置         | 字段              | 类型             | 约束                                                                                                                       |
|------------|-----------------|----------------|--------------------------------------------------------------------------------------------------------------------------|
| fields     | `array`         | string         | 逻辑字段名（camelCase，大小写敏感）；仅可点名白名单内（非 `@hidden`、未被禁用）字段。缺省（nil）= 默认全量投影（所有非 `@hidden` 列）；**显式空数组 `[]` 拒绝**（与缺省语义二分）；重复元素去重保序 |
| where      | `field`         | string         | 逻辑字段名，仅接受 camelCase 白名单拼写                                                                                                |
| where      | `op`            | string         | 见 §6.2                                                                                                                   |
| where      | `value`         | any            | 主比较值                                                                                                                     |
| where      | `rightValue`   | any            | 仅 `between` 使用，必填                                                                                                        |
| sort       | `field`         | string         | 逻辑字段名，同上拼写规则                                                                                                             |
| sort       | `order`         | string         | asc/desc 大小写不敏感，后端归一化为大写；其他值拒绝                                                                                           |
| pagination | `page`          | int            | 从 1 开始；≤ 0 拒绝；仅 offset 模式                                                                                                |
| pagination | `pageSize`     | int            | 两种模式共用的每页条数；显式提供时 ≤ 0 或超上限拒绝                                                                                             |
| pagination | `after`         | string \| null | 不透明游标；**非空即游标模式（深分页）**，见 §6.5                                                                                            |
| pagination | `includeTotal` | bool           | 是否计算 total；默认 true；false 时响应 `total` 为 null 且不执行 COUNT。该字段属 handler 层消费，不进入 Params 与查询引擎                                 |

**where 数组语义**：多条条件之间恒为 **AND**；不支持 OR 与条件分组（见非目标）。**冲突规则**：任意两个条件经翻译后命中同一
Params 目标字段（含 `between` 拆分出的 Gte/Lte 与独立 `gte`/`lte` 的重叠、同 field+op 重复出现）→ **400**，Reason 为
`conflicting conditions`，定位到后一条下标。不存在“后写覆盖先写”路径。
三个数组与 pagination 均可缺省（nil 视为空 / offset 模式首页）。信封为全资源通用结构。

### 6.2 操作符语义

| op                     | SQL 语义                    | value 形态                    | 适用 dataType                     |
|------------------------|---------------------------|-----------------------------|---------------------------------|
| `eq` / `neq`           | `=` / `<>`                | 非空标量                        | 全部                              |
| `gt` `gte` `lt` `lte`  | 对应比较                      | 标量                          | integer, number, datetime, date |
| `between`              | `>= AND <=`（含两端）          | value=左端，rightValue=右端     | 同上                              |
| `in` / `not_in`        | `IN` / `NOT IN`（展开占位符）    | 非空数组，1 ≤ len ≤ MaxIn        | 除 jsonb 外全部                     |
| `prefix`               | `ILIKE 'v%' ESCAPE '\'`   | string；v 中 `%` `_` `\` 全量转义 | string                          |
| `suffix`               | `ILIKE '%v' ESCAPE '\'`   | 同上                          | string                          |
| `contains`             | `ILIKE '%v%' ESCAPE '\'`  | 同上（任意位置匹配）                  | string                          |
| `is_null` / `not_null` | `IS NULL` / `IS NOT NULL` | 禁止携带 value 与 rightValue    | 任意 dataType，前提列可空且已显式声明         |
| `has`                  | `@>`                      | json 值                      | jsonb                           |
| `in`（int_array 列）      | `&&`（数组重叠）                | 非空数组                        | int_array                       |

**模糊三 op 的通配符契约**：通配符的位置完全由 op 语义决定（前缀 / 后缀 / 任意位置），value 永远是字面量；value 中出现的 `%`、
`_`、`\` 一律转义为字面字符。前端不存在“传 `%` 实现模糊”的口子。

### 6.3 类型收敛（严格策略）

- 数值只收 JSON number，且整数类型校验 float64 整数性；不收字符串数字。
- datetime 只收 RFC3339 字符串；date 只收 `YYYY-MM-DD`。
- 枚举只收白名单内字符串。
- 数组逐元素收敛，单数组长度超 MaxIn 拒绝。
- 未知 JSON 键拒绝；请求体上限 1MB。
- 游标内部列值解码复用同一套收敛函数（含 NaN/Infinity 哨兵还原，见 §6.5）。

### 6.4 默认限额

| 项                                       | 默认值                                                 |
|-----------------------------------------|-----------------------------------------------------|
| MaxIn（in/not_in 单数组长度）                  | 1000                                                |
| where 条数上限                              | 20                                                  |
| **占位符总预算**（所有 in/not_in 数组长度之和 + 条数）    | 60,000（远低于 PG 单语句 65535 协议上限的交叉风险面；解析期累加校验，超预算 400） |
| pageSize 默认 / 上限                       | 表注释 `@page`（全局缺省 20/200）；两种分页模式共用                   |
| offset 上限（仅 offset 模式，校验对象为归一化后 Offset） | 100,000（超限拒绝并引导改用 `after` 游标）                       |
| 游标模式                                    | 无累计翻页深度限制；单页仍受 pageSize 上限约束                       |

### 6.5 分页双模式

**模式判定（由请求显式形态决定，无隐式回退）**
| 请求形态 | 模式 | 生效参数 |
|---|---|---|
| `after` 缺省或 null | offset | page（默认 1）、pageSize |
| `after` 为非空字符串 | keyset 游标 | pageSize 作为 limit；page 必须缺省或 = 1，`page > 1` 与 `after` 混用 → 400 |
| `after` 为空字符串 | — | 400（空游标非法） |
协议门（Parse）只做上述形态判定并将 `after` 原样透传；**游标内容校验（base64/JSON 解码、指纹比对、值收敛）统一在查询函数内执行
**，typed 门直接构造 `After` 时走完全相同的路径。
**游标设计**

- 编码：`base64url(JSON{ "f": "<FNV-64 十进制字符串>", "v": [<列值>, ...] })`，`v` 为有效排序（含 tiebreaker）依序的列值；`f`
  编码为字符串避免 JS 大整数精度问题。
- 排序指纹 `f`：`FNV-64(资源名 ‖ 有效排序全序列的逻辑列名 + 方向)`。**掺入资源名**使跨表同规格排序（如多表都有
  `created_at DESC, id DESC`）的游标互不通用；换排序、跨表、跨能力视图复用旧游标 → 指纹不符 → 400。指纹不含 pageSize。
- **值编码**：datetime 为 RFC3339、date 为 `YYYY-MM-DD`、number 中的 `NaN` / `Infinity` / `-Infinity` 编码为同名**字符串哨兵
  **（JSON 无法原生表示，Go json.Marshal 对 NaN 直接报错），解码时还原并仍走类型收敛；其余类型原生 JSON。
- 不签名：游标仅定位“调用方本就可发起的同一查询”的读取位置，不构成权限边界；解码后所有值仍走参数化占位符与严格类型收敛，篡改最坏导致空集或
  400。
- 值收敛：游标列值逐个经类型收敛函数与当次有效排序列的 dataType 比对，不符 → 400。
  **有效排序解析（查询函数内统一执行）**

1. `params.OrderBy` 非空 → 校验每个字段 ∈ registry、已声明 `@sortable`、未被 opts 禁用（否则 error）。
2. 为空 → 回落表级 `@default_sort`。
3. 末列若非主键 → 无条件追加主键 ASC 作 tiebreaker；末列已为主键则不重复追加。tiebreaker 保证全序，使 OFFSET 翻页与游标定位双重确定。
4. 生成的 ORDER BY **显式写出 NULLS 方位**：ASC → `NULLS LAST`，DESC → `NULLS FIRST`（即 Postgres 默认方位，显式写出以与
   seek 模板一一对应、杜绝歧义）。
   **seek 谓词（精确规范）**
   有效排序 `S = [(c1,d1), …, (cn,dn)]`（cn 为主键，非空），游标值 `V = (v1,…,vn)`。定位“严格在游标行之后”：

```
P = K(c1,v1) ∨ ( E(c1,v1) ∧ K(c2,v2) ) ∨ … ∨ ( E(c1,v1) ∧ … ∧ E(cn-1,vn-1) ∧ K(cn,vn) )
```

- **空值安全等值** `E(x, v)` = `x IS NOT DISTINCT FROM v`——游标值本身为 NULL 时，普通 `=` 恒为 NULL 会导致漏行，必须用空值安全等值。
- **方向化严格比较** `K(x, v)` 按 NULLS 方位分情形：
  | 方向 | v 为 NULL | v 非 NULL |
  |---|---|---|
  | ASC（NULLS LAST） | `FALSE`（NULL 组之后无行，整项模板期消除） | `x > v OR x IS NULL` |
  | DESC（NULLS FIRST） | `x IS NOT NULL` | `x < v` |
- 末列为主键（非空），故 `E(cn,vn)` 退化为普通等值、`Kn` 退化为普通 `>` / `<`。
- 全部 vi 经参数化占位符传入；展开式用 squirrel Or/Eq/Expr 组合。
  **响应信封**

```json
{
  "items": [],
  "total": 0,
  "page": 1,
  "pageSize": 20,
  "nextCursor": "…",
  "hasMore": true
}
```

- `total` 来自 `CountUsers`（只吃过滤 WHERE，不含 seek），由 handler 依 `includeTotal` 决定是否调用；false 时 `total` 为
  null。**total 与 items 来自独立查询，非强一致快照**；强一致需求由调用方以同一事务的 DBTX 注入两个调用。
- `hasMore` / `nextCursor`：`LIMIT pageSize + 1` 取回，超出即 hasMore = true；`nextCursor` 由第 pageSize 行的**有效排序列值
  **构造——由投影并集不变量保证这些列必然在 SELECT 中。
- **offset 模式同样返回 `nextCursor`**：客户端首页正常请求，随时可携带 nextCursor 无缝切换游标模式续翻；游标模式 `page` 为
  null。
- 窄投影（`fields` 收窄）时，响应可能包含被强制并入的排序列值（必属非 `@hidden` 列）；前端按 `fields` 语义消费，handler
  亦可按需过滤输出。

---

## 7. 类型系统

### 7.1 类型映射

| PG 类型             | dataType  | Params Go 类型                                                         |
|-------------------|-----------|----------------------------------------------------------------------|
| int2/4/8, serial  | integer   | int64                                                                |
| numeric, float4/8 | number    | float64                                                              |
| timestamptz       | datetime  | time.Time                                                            |
| timestamp（无时区）    | datetime  | time.Time（语义为数据库本地时间；收敛与游标编码按无时区字面量处理。**新表建议一律 timestamptz**，避免时区歧义） |
| date              | date      | time.Time                                                            |
| varchar, text     | string    | string                                                               |
| bool              | boolean   | bool                                                                 |
| uuid              | uuid      | string                                                               |
| enum              | enum      | 块 1 模型类型（自动收集 import）                                                |
| jsonb             | jsonb     | []byte                                                               |
| _int4             | int_array | []int64                                                              |

### 7.2 能力矩阵（类型 × 默认可用 op）

| dataType                        | eq/neq | in/not_in   | gt/gte/lt/lte/between | contains/prefix/suffix | has |
|---------------------------------|--------|-------------|-----------------------|------------------------|-----|
| integer, number, datetime, date | ✓      | ✓           | ✓                     | —                      | —   |
| string                          | ✓      | ✓           | —                     | ✓                      | —   |
| boolean, uuid, enum             | ✓      | ✓           | —                     | —                      | —   |
| jsonb                           | —      | —           | —                     | —                      | ✓   |
| int_array                       | —      | in（`&&` 语义） | —                     | —                      | —   |

**null op 与类型矩阵正交**：`is_null` / `not_null` 不进入上表、不随类型或可空性自动授予。任意 dataType
的列都可以显式声明它们，唯一前提是列本身可空；是否声明完全由 DSL 决定。最终 op 集合 = 类型能力 ∩ DSL 声明。

### 7.3 字段键

- **唯一逻辑键**：camelCase，由物理列名 snake_case 转换。协议与 typed 门**只接受 camelCase 拼写**（大小写敏感）；snake_case
  物理列名不是合法输入——物理列名重命名不构成协议面变更，逻辑键才是对外契约。
- camel 折叠冲突（两列转出同一 camel）→ 生成期 fail。
- 生成的字段常量结构与 registry 一一对应，供禁用选项与 OrderBy/SelectFields 引用。

### 7.4 overrides 盲区

插件看不到 sqlc-gen-go 的 `go_type` overrides。缓解优先级：动态列避开 override 列 → options
`go_type_overrides` 镜像 → 迁移 fork 模式。
---

## 8. 生成物契约

按表生成，每表四件套 + 字段常量，落于独立子包。**文件命名规则**：统一加 `_dynamic_gen` 后缀（如 `users_dynamic_gen.go`
），与其他生成器或手写代码在物理层清晰区分。
**命名约定（表 users 为例）**：
| 产物 | 规则 | 示例 |
|---|---|---|
| Params | `List` + 复数资源 + `Params` | `ListUsersParams` |
| 查询函数 | 动词 + 复数资源 | `ListUsers` / `CountUsers` |
| 解析函数 | `Parse` + 复数资源 + `Query` | `ParseUsersQuery` |
| schema 函数 | 复数资源 + `QuerySchema` | `UsersQuerySchema` |
| 字段常量 | 单数实体 + `Field` | `UserField` |

### 8.1 registry 与字段常量

- 字段表（逻辑键、物理列、dataType、op 集、sortable、selectable、hidden）、单键（camel）索引 map、枚举值切片。同一份结构同时驱动解析白名单、禁用校验与
  schema 输出。
- **字段常量**，供运行期禁用选项、OrderBy、SelectFields 引用；IDE 可补全，拼错无法通过编译：

```go
var UserField = struct {
ID        dynq.FieldName
Name      dynq.FieldName
Status    dynq.FieldName
CreatedAt dynq.FieldName
}{ "id", "name", "status", "createdAt" }
```

禁用选项的目标若不在本表 registry，在入口返回 error 显式暴露编程错误——禁用是权限收窄手段，静默 no-op 会制造安全假象。

### 8.2 ListUsersParams（typed 结构体）

命名约定（后缀即语义）：
| 后缀 | 来源 op |
|---|---|
| 无后缀 | eq |
| `Neq` / `In` / `NotIn` | neq / in / not_in |
| `Gt` `Gte` `Lt` `Lte` | gt / gte / lt / lte；between 落到 Gte + Lte 两字段 |
| `Contains` / `Prefix` / `Suffix` | contains / prefix / suffix（零值 `""` = 不参与） |
| `IsNull bool` / `NotNull bool` | is_null / not_null（各自独立字段，false = 不参与，非指针；同时为 true → error） |
| `OrderBy []dynq.OrderBy` | 元素为**逻辑字段名 + 方向**的 typed 结构（见下），物理映射收敛在生成物内部 |
| `Limit` / `Offset uint32` | offset 模式归一化结果；typed 门 Limit=0 视为采用表默认 pageSize |
| `After *string` | 游标模式不透明游标；nil = offset 模式；与 `Offset > 0` 互斥 |
| `SelectFields []dynq.FieldName` | 动态投影字段列表（**逻辑键**）；nil = 默认全量投影 |

```go
// dynq 中的排序结构（运行时库定义，生成物引用）
type OrderBy struct {
Field FieldName // 逻辑字段名，可用生成常量（如 UserField.CreatedAt）
Desc  bool      // false = ASC
}
```

区间与集合字段用 `*T` / 切片，nil / 空 = 不参与过滤。`After` 与 `Offset` 由 Parse 严格二选一产出；typed 门直接构造时由共享形态复核把关。
**typed 结构体单字段单值，天然无法表达协议门拒绝的条件冲突**——冲突检测只存在于协议门。

### 8.3 查询函数

```go
func ListUsers(ctx context.Context, db DBTX, params ListUsersParams, opts ...dynq.QueryOption) (dynq.Result[model.User], error)
func CountUsers(ctx context.Context, db DBTX, params ListUsersParams, opts ...dynq.QueryOption) (int64, error)
```

**ListUsers 内部步骤**：

1. opts → `QueryConfig`（禁用目标对照 registry，未知含跨表误用 → error）。
2. `validateUsersParams(params, cfg)`——**共享形态复核**（List / Count 同一份）：
    - where 域：params 中禁用字段携带非零值 → error（显式拒绝，不静默剔除）；
    - sort 域：OrderBy 含禁用或不可排序字段 → error；
    - select 域：SelectFields 含禁用、`@hidden` 或未知字段 → error；
    - `IsNull` 与 `NotNull` 同真、`After` 与 `Offset > 0` 并存 → error；
    - Limit 超上限、offset 模式 Offset 超上限 → error。
3. **有效排序解析**（§6.5）：用户排序 / `@default_sort` 回落 / 主键 tiebreaker / 显式 NULLS 方位。
4. **游标校验**：`After` 非空 → 解码、指纹比对（资源名 + 有效排序）、列值逐个类型收敛比对 dataType；不符 → error（Kind =
   cursor）。此为双门唯一校验路径。
5. `buildUsersSelect(params, cfg) []string`——**投影并集**：
   `(SelectFields 映射的物理列；nil → 全部非 hidden 列) ∪ 有效排序列（含 tiebreaker)`，去重。排序列强制并入是游标构造与
   hasMore 探测的前提；sortable 蕴含非 hidden，故并入集不可能含敏感列。
6. `buildUsersWhere(params, cfg) []sq.SqlBuilder`——过滤条件，List / Count 唯一事实源。
7. `buildUsersSeek(...)`——游标定位谓词（§6.5 模板），仅游标模式追加。
8. `LIMIT Limit + 1` + ORDER BY 执行；结果扫描用 `pgx.CollectRows(rows, pgx.RowToStructByNameLax[model.User])`（窄投影缺列
   留零值；按值返回 `[]model.User`，与 `Result.Items []T` 匹配；pgx struct tag 完成列名→字段映射）。
9. 由第 Limit 行构造 `NextCursor` / `HasMore`。
   **CountUsers**：执行步骤 1、2 + `buildUsersWhere` + `COUNT(*)`；忽略投影、排序与游标（参数形态错误仍由步骤 2 拦截）。

- **排序注入防护**：仅白名单拼接；无有效排序时回落 `@default_sort`；tiebreaker 保证确定性。
- **窄投影零值语义**：未查询的列在结果结构体中为零值，调用方无法区分“未查 / NULL / 真零值”——这是动态投影的固有语义；需要区分时由
  handler 按请求 fields 做响应 DTO 化，或退回固定查询。
- **观测性边界**：查询函数不内置日志/统计 Hook；经业务层装饰器或包装 DBTX 实现。

### 8.4 ParseUsersQuery（协议门）

输入 `*dynq.Query` 与 opts，输出 typed Params。生成代码为穷举 switch（零反射、IDE 可跳转），职责：

1. 应用 opts → `dynq.QueryConfig`（集合去重，幂等）；禁用目标不在 registry → error。
2. **fields 解析**：显式空数组 → 400；逻辑键 → registry 索引（未知拒绝；`@hidden` → 400；select 域禁用 → 400）；去重后以**逻辑键
   **填入 `SelectFields`（物理映射留待查询函数）。
3. where field → registry 索引（未知拒绝；where 域禁用 → 400 "field disabled in this context"）。
4. op ∈ 字段 op 集（越权拒绝）。
5. 严格类型收敛（委托 dynq）；占位符预算累加校验（§6.4）。
6. **冲突检测**：条件翻译目标字段重复（含 between 拆分与独立 gte/lte 重叠）→ 400 `conflicting conditions`；between → Gte+Lte
   拆分；is_null / not_null → 独立 bool 字段并做互斥校验。
7. sort 白名单校验（不可排序 / 禁用 → 400）+ 归一化为 `[]dynq.OrderBy`（逻辑键）；默认排序回落与 tiebreaker
   不在此做，统一由查询函数解析（与游标指纹同源）。
8. 分页模式判定 + 归一化（page→Offset、pageSize→Limit）；pageSize ≤ 0 / 超上限、offset 超上限 → 400。
9. `after` 形态判定（非空即游标模式、空串 400、与 page>1 互斥 400）后**原样透传**，内容校验在查询函数。

### 8.5 UsersQuerySchema（前端契约出口）

```go
func UsersQuerySchema(opts ...dynq.QueryOption) dynq.ResourceSchema
```

返回 `dynq.ResourceSchema`：资源名、字段列表（field、label、dataType、可空、sortable、selectable、op 集、枚举值）、
默认排序、分页参数（含双模式与 includeTotal 说明）。不携带 ui 字段——渲染规则见 §10.2。

- 与 registry 由同一模板循环产出，再经 opts 做与 Parse 完全相同的减法投影：where 禁用 → 该字段 op 集清空；sort 禁用 →
  sortable = false；select 禁用 → selectable = false。字段被完全禁用（或原 `@hidden`）则不出现在 schema 中。
- **幂等性**：Schema 是 `(生成期元数据, opts)` 的确定性纯函数——同一 opts 切片产出字节级相同的 schema；内容 hash 作
  ETag，不同禁用视图自然得到不同 ETag。
- 前端拿到的即有效能力全集：被禁用字段根本不出现在 schema 中，QueryBuilder 无需感知“禁用”概念。

### 8.6 文件头

生成文件头部回显策略来源（本表各列的 `@dynamic` / `@sortable` / `@hidden` /
`@selectable` 声明清单），使 DDL 注释与生成物的对应关系在 review 时可直接比对。
---

## 9. 运行时库 dynq（手写，不生成）

协议是表无关的，协议机器也表无关：约 400 行手写包承载全部运行时逻辑与单测，生成代码只保留表相关数据与 switch。
**职责**：

- 信封类型定义（含 `includeTotal`）、严格解码（`MaxBytesReader` + 未知键拒绝）。
- 操作符归一化（`Order`）、分页归一化（`Paginate`）、占位符预算校验辅助。
- `OrderBy` 结构、`FieldName` 类型。
- **游标编解码**：`EncodeCursor` / `DecodeCursor`（base64url + JSON + FNV-64 指纹；指纹掺资源名；`f` 字符串编码；number 的
  NaN/±Inf 哨兵编解码；列值复用类型收敛函数）。
- 各 dataType 的标量/切片类型收敛函数（`AsInt64` / `AsTime` / `AsEnum` / `AsInt64Slice` …）。
- **`QueryOption` / `QueryConfig`**：三域禁用集合（where / sort / select），集合语义（幂等、交换、去重），`WhereDisabled` /
  `SortDisabled` / `SelectDisabled` 查询；禁用目标合法性由生成物对照 registry 校验。
- **`Result[T]`**：`{ Items []T; HasMore bool; NextCursor string }`。
- 结构化错误构造。
  **错误模型**：

```go
type Error struct {
Kind   string // "where" | "sort" | "pagination" | "cursor" | "option" | "fields"
Index  int    // 数组下标
Field  string
Reason string // 如 "unknown field" / "op not allowed" / "field disabled in this context"
//     / "conflicting conditions" / "field hidden" / "fingerprint mismatch"
}
```

**列表响应信封**：
`{ items, total, page, pageSize, nextCursor, hasMore }`（游标模式 page 为 null；includeTotal=false 时 total 为 null）。
---

## 10. 前端 schema 与 UI 推断

### 10.1 交付方式

- `GET /{resource}/_query-schema`：返回 `ResourceSchema`；对 opts 投影后的内容 hash 作 ETag，可长缓存；生产环境对每端点的固定
  opts 预序列化一次。
- `POST /{resource}/list`：协议信封走 body（含数组与嵌套，不适合 GET 拼参）。
- **接线纪律**：`_query-schema` 与 `list` 两个 handler 必须引用同一 opts（推荐包级单一来源变量），review 检查点。

### 10.2 前端渲染规则（契约，固化进文档）

schema **不携带 `ui` 字段**：控件族由 `dataType` 唯一确定，操作符下拉状态 = 同层 `ops` 中除
is_null/not_null 的全部 op（该两类为 API-only 不渲染），值控件形态由所选 op 决定（between → 双控件、
in/not_in → 多值输入、其余 → 单值输入）。后端不复制、不过滤 ops —— 顶层 `ops` 是操作符渲染的唯一
事实源，渲染信息全部可由 `(dataType, ops, sortable, selectable)` 派生，不存在“渲染漏掉顶层 op”的口子。

| dataType             | 控件族    | 说明                                |
|---------------------|--------|-----------------------------------|
| string / uuid       | 输入框    | uuid 格式预校验由 dataType 提示           |
| integer / number    | 数字输入   |                                   |
| datetime / date     | 时间/日期选择 |                                   |
| enum                | 下拉选择   | 单选/多选形态由所选 op 决定，options = schema.enum |
| boolean             | 布尔选择   |                                   |
| int_array           | 标签输入   |                                   |
| jsonb、仅 is_null/not_null 的字段、where 域整体禁用 | 不渲染 | 留给 API 直调方 |

前端表格列头依赖 `selectable` 属性（纯 UI 提示，见 §5.3 真值表）：`selectable = true` 的字段渲染列头并支持点击隐藏/显示该列；
`selectable = false` 的字段默认不作为列展示，仅作为过滤条件可用。分页组件按 `pagination.modes` 渲染：含 offset → 页码跳转；含
cursor → 无限滚动；两者皆含 → 混合分页；total 为 null 时隐藏“共 N 条”。

### 10.3 @api 契约（引用字段选项数据源）

`@api` 服务于**引用型 ID 选择器**（user_id / assignee_id 等）：该字段 where 值控件的 options 经 `GET {endpoint}` 拉取，
schema 输出字段级 `api` 对象：

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

- **`endpoint`** 相对前端 baseURL；`valueKey` / `labelKey` 为响应 `items` 每项的顶层字段名，值类型与字段 dataType 一致。
- **`mode`** 为最终交互模式：显式声明优先；否则由 ops 推导——含 `in` → `multi`、含 fuzzy 三 op → `search`、否则 `single`。
  **任意 mode 均支持 `page/pageSize` 翻页**（分页是选项接口的通用能力，与 mode 无关）；`search` 模式前端额外携带 `q` 关键字。
- **响应信封**（全资源统一）：`{"items": [{"<valueKey>": ..., "<labelKey>": ...}], "hasMore": false}`；`hasMore=true` 时前端携带 `page+1` 继续拉取。两种实现均可：
  - **复用生成方法**：`ListUsers(ctx, db, ListUsersParams{NameContains: q, Limit: pageSize})` → `items` 即 `Result.Items`
    （每行模型，字段名天然匹配 `valueKey` / `labelKey`，值类型编译期对齐）；
  - **独立 options 接口**（推荐生产使用：脱敏、限字段、缓存）。
- `@api` 是纯前端契约：不参与服务端校验路径（ID 值仍走类型收敛 + 参数化）；与 `@hidden` 组合、非 integer/uuid/string 列、
  缺 `value_key`/`label_key`、未知参数、endpoint 含 `?` → 生成期 fail（见 §11 校验总表）。

### 10.4 前端消费模式

通用 QueryBuilder 组件吃 schema：遍历 fields 渲染操作符下拉与对应控件、sortable 字段挂表格列头排序、pagination.options 渲染每页条数选择器。一次开发，全部列表页复用；端点级能力差异（禁用）对前端透明。QueryBuilder 不提供条件分组（OR）能力，与协议约束一致。
---

## 11. 校验规则总表（全部为显式拒绝，无静默修正）

| 违规                                                                       | 结果                                     |
|--------------------------------------------------------------------------|----------------------------------------|
| 未知 field / 非 camelCase 白名单拼写                                             | 400（where/sort/fields 定位到下标）           |
| op 不在该字段能力集内                                                             | 400                                    |
| **两个条件翻译命中同一 Params 目标字段（含 between 与 gte/lte 重叠、同 field+op 重复）**         | 400（`conflicting conditions`）          |
| fields 包含 `@hidden` 字段或 select 域禁用字段                                     | 400                                    |
| fields 显式空数组                                                             | 400                                    |
| field 在本调用点被禁用                                                           | 400（"field disabled in this context"）  |
| eq/neq 携带 null value                                                     | 400，指引用 is_null / not_null（不翻译、不推导）    |
| is_null/not_null 携带 value 或 rightValue                                  | 400                                    |
| 同字段 is_null 与 not_null 并存（跨条件）或 IsNull/NotNull 同真                        | 400 / error                            |
| in/not_in 空数组                                                            | 400                                    |
| 数组超 MaxIn / where 条数超上限 / **占位符总数超预算**                                   | 400                                    |
| 不可排序字段出现在 sort                                                           | 400                                    |
| 被禁用排序字段出现在 sort                                                          | 400                                    |
| order 非 asc/desc                                                         | 400                                    |
| page ≤ 0 / **pageSize 显式提供且 ≤ 0** / pageSize 超上限                       | 400                                    |
| offset 模式 Offset 超上限                                                     | 400（引导改用 `after` 游标或收窄筛选）              |
| after 与 page > 1 混用 / after 为空串                                          | 400                                    |
| 游标解码失败 / 列值类型不符（含哨兵还原失败）/ 排序指纹不符（含跨表复用）                                  | 400 / error（Reason 精确定位，Kind = cursor） |
| 类型不符（字符串数字等）                                                             | 400                                    |
| 未知 JSON 键 / 请求体超 1MB                                                     | 400                                    |
| opts 禁用目标不在 registry（含跨表误用）                                              | 调用方 error（编程错误显式暴露，非 400）              |
| typed params 命中禁用字段（where 值 / OrderBy / SelectFields）                    | error                                  |
| After 与 Offset 并存（typed 门）                                               | error                                  |
| Limit / Offset 超上限（typed 门）                                              | error                                  |
| DSL 声明非法（类型不支持、非空列声明 null op、camel 冲突、@default_sort 指向不可排序列、@page 默认超上限） | **生成期 fail**                           |
| DSL 方括号内未知 `@` 标签 / 方括号未闭合 / **`@hidden` 与任一正向标记组合**                     | **生成期 fail**                           |
| `@api` 声明非法（非 integer/uuid/string 列、缺 value_key/label_key、未知参数、mode 非法、endpoint 含 `?`） | **生成期 fail**                           |

---

## 12. 安全设计

1. 所有值经占位符参数化，无任何用户值进入 SQL 文本——包括游标内的列值与 NaN/Infinity 哨兵值。
2. **投影白名单严格校验**：ORDER BY 的列名与方向、SELECT 的列名、WHERE 的 field、op 四者全部白名单，白名单在生成期固化为代码字面量；无
   `Sprintf` 直拼路径；seek 谓词的列名与方向来自同一白名单的有效排序。
3. **敏感字段全能力隔离**：`@hidden` 列不参与动态层任何能力——物理上不可能出现在 SELECT 子句、WHERE 过滤、ORDER BY 排序、前端
   schema、游标值中（sortable ⇒ 非 @hidden，保证强制并入投影的排序列不含敏感列）；`@hidden` 与正向标记组合在生成期即 fail。
4. **模糊三 op 的 value 全量转义** `%`、`_`、`\` 后再拼 ILIKE 模式，通配符位置由 op 决定——用户输入不可能注入通配符。
5. 物理列名不进入任何对外输入输出；snake_case 不是合法协议拼写；游标只含排序指纹（掺资源名的哈希）与列值，不含列名。
6. 游标不签名：游标仅定位“调用方本就可发起的同一查询”的读取位置，不构成权限边界；解码值经严格类型收敛 + 参数化，篡改最坏导致空集或
   400。
7. **运行期禁用是纯减法**：选项无法开启生成期未声明的 op / 排序 / 投影，能力上限永远由 DSL 决定；禁用选项经 registry
   校验，静默失效被显式 error 取代，杜绝“以为禁了实际没禁”的权限假象。
8. **条件冲突显式拒绝**：同字段同 op 重复、between 与独立区间条件重叠等一律 400，不存在后写覆盖先写导致的语义偏移。
9. 请求体大小限制、未知键拒绝、占位符总预算构成输入面收口；解析路径零反射。

---

## 13. 工程化与 CI

- **Makefile**：`sqlc`（构建插件 + 全量生成）、`check`（生成后 `git diff --exit-code` 锁 gen 目录，防漂移）、`release-wasm`
  （编译 + sha256）。
- **生成物提交入库**：契约变更直接进 PR diff，评审强制可见。
- **sqlc 版本 pin**：CI 用固定版本镜像执行 `make check`。
- **测试策略**：
    - dynq 全量单测：三域禁用集合的幂等/交换/未知目标、游标编解码往返（**含 NaN/±Inf 哨兵**）、指纹比对（**含跨表同排序规格互斥
      **）、类型收敛、冲突检测辅助。
    - 插件 golden file 测试：
        - 动态投影代码生成（**含并集规则：窄 fields + 排序列强制并入**）；
        - 游标 seek 谓词快照：单列排序 + tiebreaker、混合方向、**可空列 NULL 分支**、**首列游标值为 NULL**、**全 NULL 边界行
          **；
        - where 条件冲突检测的 Parse 代码；
        - `@hidden` 组合等 DSL 非法声明的**生成期错误快照**；
        - opts 投影（重复/乱序/未知目标/跨表）；
        - 禁用视图下的 schema 快照。
    - 手写首版生成物作为模板校准基准。
- **升级纪律**：见 §4.5 固定顺序。

---

## 14. 已知限制与风险

| 项             | 说明与对策                                                                                                              |
|---------------|--------------------------------------------------------------------------------------------------------------------|
| 仅单表           | JOIN/CTE/聚合走 sqlc 静态查询；需要动态过滤的“表”用 Postgres VIEW 包装                                                                |
| 无 OR / 条件分组   | 协议级约束；需要 OR 的查询走 sqlc 静态定义                                                                                         |
| 同字段多条件受限      | 同字段不同 op（目标字段不同）可并存；命中同一目标字段的组合被拒绝。如未来需支持，将 Params 对应字段升级为切片，协议数组语义不变，仅动模板                                         |
| Catalog ≠ 真实库 | 生成列、部分索引表达式等 DDL 解析支持有限；迁移文件即 schema.sql 单一事实源 + sqlc vet 兜底                                                       |
| 未传 vs NULL    | 指针 / bool 二值区分；如需第三态升级 `sql.Null[T]`，仅动模板                                                                          |
| **窄投影零值歧义**   | 未查询列 = 结构体零值，与 NULL/真零值不可区分；由 handler 按请求 fields 做响应 DTO 化，或退回固定查询                                                 |
| total 非强一致    | total 与 items 为独立查询；深翻页用 includeTotal=false 关闭 COUNT 控制成本；强一致需求注入同一事务 DBTX                                        |
| 游标不能跳页        | keyset 只能顺序续翻；随机跳页仍走 offset 且受 100,000 上限；前端混合分页缓解                                                                 |
| 反向游标未支持       | `before` 为协议预留位；需要双向无限滚动的场景暂缓                                                                                      |
| 游标对排序变更敏感     | 用户改排序后旧游标指纹不符 → 400（显式失效优于静默错位）                                                                                    |
| seek 谓词索引利用   | OR 展开与 `IS NOT DISTINCT FROM` 在多列排序下可能无法充分利用索引；常见“单列排序 + 主键 tiebreaker”场景的首项为纯范围比较，可走排序索引；深翻页性能依赖 `(排序列, 主键)` 复合索引 |
| timestamp 无时区 | 语义为数据库本地时间；新表一律 timestamptz                                                                                        |
| OpenAPI 盲区    | 静态文档无法表达字段组合约束，由 `_query-schema` 动态发现补偿                                                                            |
| squirrel 维护模式 | 使用收敛于生成物内部；替换只改模板不动业务层                                                                                             |
| overrides 盲区  | 见 §7.4 缓解优先级                                                                                                       |
| 注释 DSL 可见性    | 策略在 DDL 中，IDE 不可跳转；以生成文件头回显补偿                                                                                      |
| 禁用选项为字符串键     | 经生成字段常量缓解拼错；运行期 registry 校验兜底                                                                                      |
| pgx 结构体扫描依赖反射 | 动态投影要求列数可变，放弃生成代码显式 `Scan`，改用 `pgx.RowToStructByName`（按值返回，匹配 `Items []T`），性能损耗经 pgx 内部优化可接受                       |

---

## 15. 决策记录

**已定**

- 独立插件起步 / process 开发 + wasm 发布 / 元数据取 Catalog / DSL 载体为列注释 / `default_ops: none` / between 含两端 /
  空 in 拒绝 / 严格类型收敛 / eq+null 拒绝 / 排序数组化多字段 + 白名单 / 主键 tiebreaker 无条件追加 / 过滤 WHERE 共享 /
  生成物提交入库。
- **where 恒为 AND 语义，无 OR/分组**；条件冲突（同一 Params 目标字段被两个条件命中）显式 400，无覆盖路径。
- 分页双模式：offset 保留并维持上限；keyset 游标为一等公民；游标 base64url 自包含 + FNV-64 排序指纹（**掺资源名**、`f`
  字符串编码、number 哨兵编码）、不签名；**游标内容校验单点于查询函数，双门同路径**；seek 用展开式谓词 +`IS NOT DISTINCT FROM`
  空值安全等值 + 显式 NULLS 方位分情形比较；LIMIT n+1 探测 hasMore；offset 模式同样返回 nextCursor 支持无缝切换。
- **投影并集不变量**：实际 SELECT ⊇ 有效排序列（含 tiebreaker），任何 fields 收窄下游标构造恒成立。
- **`includeTotal`**：默认 true；false 时跳过 COUNT、total 为 null；total 非强一致语义文档化。
- 模糊分解为 prefix / suffix / contains 三个独立 op；预设名 `fuzzy`；value 全量转义，通配符契约由 op 决定。
- is_null / not_null 完全独立：不入预设、不因可空自动授予、不从 eq/neq 推导；Params 拆 IsNull / NotNull 并互斥校验。
- **`@hidden` 为全能力排除标记**：与 @dynamic/@sortable/@selectable 组合生成期 fail；hidden 列不参与投影/点名/过滤/排序/schema/游标。
  **`@selectable` 为纯 UI 提示**，fields 点名白名单 = 非 hidden 列。
- **DSL 边界规则**：方括号内容不以 `@` 起始则整体归入 label（人类注释方括号不误伤）；未知标签 / 未闭合 / 非法组合生成期
  fail。
- **协议仅接受 camelCase 逻辑键**；snake_case 物理列名不是输入面；`OrderBy` / `SelectFields` 均携带逻辑键，物理映射收敛在生成物内部。
- **占位符总预算 60,000**，解析期累加校验。
- 全部生成方法签名统一 `opts ...dynq.QueryOption`；禁用纯减法、集合幂等；Parse / List / Count / Schema 四点同参；schema
  幂等反映禁用；禁用命中显式拒绝；未知禁用目标 error。
- List 返回 `dynq.Result[T]`（Items / HasMore / NextCursor）；扫描用 `pgx.RowToStructByNameLax`（窄投影缺列留零值）。
- 观测性边界：不内置 Pre/Post Hook，经业务层装饰器或包装 DBTX 实现。
- 产物隔离：示例工程独立 `example/` 目录；生成物统一 `_dynamic_gen` 后缀；命名约定表（§8）冻结。
- **`@api`（引用字段选项数据源）**：仅 integer/uuid/string 列；endpoint + value_key/label_key 必填；mode 缺省由 ops 推导
  （in→multi、fuzzy→search、否则 single）；endpoint 禁 `?`；未知参数 / 类型不支持 / 与 @hidden 组合 → 生成期 fail；
  纯前端契约不参与服务端校验；响应信封 `{items, hasMore}` 全资源统一，可复用 ListXxx 或独立 options 接口。
  **待确认**
- before 反向游标与游标加签的实际需求。
  **已消解**
- `UI.Props` 形态：schema 不再携带 `ui` 字段——控件族由 `dataType` 唯一确定，操作符渲染唯一事实源为顶层 `ops`（见 §10.2）。

---

## 16. 实施路线图

1. **dynq 运行时库 + 全量单测**：信封/收敛/错误/占位符预算 + 三域 QueryOption 投影 + OrderBy 结构 + 游标编解码（哨兵、掺资源名指纹）与
   seek 构造辅助（含 NULL 分情形模板）。
2. **手写最复杂一张表的全套生成物**，冻结五份契约——协议信封（含 includeTotal、冲突规则）、响应信封、schema 形态、seek
   谓词模板、投影三标签真值表——前端凭 mock 并行开发。
3. **dump CodeGenRequest**，完成验证。
4. 插件骨架：options 解析 + Catalog 遍历 + 空模板，process 模式跑通链路。
5. 补全：类型映射表 + DSL 解析（边界隔离、null op 独立性、fuzzy 预设、hidden 互斥 fail、default_sort 校验）+
   registry/字段常量/Params/Parse（含冲突检测）/Schema/List（含有效排序解析、游标单点校验、投影并集）/Count 模板 + opts
   四点接线 + golden file 测试（含窄投影×游标、NULL 游标值、冲突、hidden 组合 fail、跨表游标互斥）。
6. `make check` 进 CI。
7. 团队稳定后发 wasm release，sqlc.yaml 切 `url + sha256`。
8. 前端通用 QueryBuilder + 混合分页 + 动态列组件。

---

## 17. 目录结构与交付物

示例/测试用的 DDL 与固定查询完全独立到 `example` 目录，与插件及运行时核心代码物理隔离；本插件生成的代码文件统一追加
`_dynamic_gen` 后缀以醒目标识。

```text
tools/sqlc_gen_dynamic/     # 插件本体：entry_process.go / entry_wasm.go / gen.go / 模板
dynq/                     # 运行时库（手写，含全部运行时单测：收敛/游标/QueryOption/冲突检测）
example/                    # 示例与测试用独立工程
 ├── db/schema.sql          # DDL + 注释 DSL（示例单一事实源）
 ├── db/queries.sql         # 固定形态查询示例
 ├── db/sqlc.yaml           # sqlc 配置（挂载本插件与 sqlc-gen-go）
 └── db/gen/                # 生成物目录
      ├── models.go         # sqlc-gen-go 产物（模型与固定查询示例）
      └── users_dynamic_gen.go  # 本插件产物（registry / 字段常量 / Params / Parse / Schema / List / Count）
```

---

## 附：术语表

| 术语          | 定义                                                           |
|-------------|--------------------------------------------------------------|
| 逻辑键         | 对外暴露的字段名，camelCase，经 registry 映射到物理列名；协议与 typed 门的唯一合法拼写     |
| 预设          | DSL 中若干 op 的命名集合                                             |
| 双门          | 协议门（JSON，运行期校验）与 typed 门（结构体，编译期）两个入口共用同一引擎                  |
| 三投影         | 协议白名单、typed Params、前端 schema 为同一元数据的三个生成产物                   |
| tiebreaker  | 追加在用户排序之后的主键排序，保证分页确定性与游标全序                                  |
| 深分页         | 翻页深度超出 offset 上限的场景，由游标模式承载                                  |
| 游标          | 不透明续翻凭证：base64url(JSON{排序指纹， 列值…})，与排序绑定                     |
| 排序指纹        | 资源名 + 有效排序列清单（列 + 方向）的 FNV-64 稳定哈希，用于游标失效检测与跨表互斥             |
| seek 谓词     | 游标定位用的展开式比较条件（空值安全等值 + 方向化比较），仅作用于 List，不参与 Count            |
| 能力投影        | 生成期白名单经运行期禁用选项的纯减法结果，四消费点同参                                  |
| 运行期禁用       | QueryOption 机制，按调用点关闭部分 where / sort / select 字段，schema 幂等反映 |
| 动态投影        | 运行期根据入参动态拼装 SELECT 子句的能力；实际投影 = 请求字段 ∪ 有效排序列（投影并集不变量）        |
| 条件冲突        | 两个 where 条件翻译命中同一 Params 目标字段，协议级显式拒绝                        |
| 占位符预算       | 单条语句参数化占位符总数上限（60,000），由 where 条数与 MaxIn 联合约束                |
| 哨兵编码        | 游标 JSON 中 number 型 NaN/±Inf 的字符串替代表示，解码还原后仍走类型收敛             |
| golden file | 以固定输入跑生成器并与快照比对的测试方式                                         |

