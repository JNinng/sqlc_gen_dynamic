# 注释 DSL 参考

sqlc\_gen\_dynamic 的全部生成能力声明在 DDL 注释中,随迁移版本化。本文档是 DSL 的完整参考:语法规则、列级/表级指令、类型映射、能力矩阵、预设展开与校验规则。

- 查询入参契约、响应信封 → 见 [README](./README.md);前端 Schema 契约 → 见 [schema.md](./schema.md);行级数据权限机制 → 见 [scope.md](./scope.md)
- 外部工程接入(sqlc.yaml 接线、options) → 见 [usage.md](./usage.md)
- 设计原理与边界论证 → 见 [desc.md](./desc.md)
- 端到端示例 → 见 [example/db/schema.sql](./example/db/schema.sql)

## 0. 术语表

| 术语                   | 含义                                                                                                                                          |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| **`fields`** **列选择** | 请求参数 `fields` 指定本次返回哪些列(等价于 SQL `SELECT` 的列列表)。不传 = 返回全部非 `@internal` 列;显式传空数组 `[]` 被拒绝;未选中的列在结果里是零值,与 NULL / 真零值不可区分                       |
| **`fields`** **可选中** | 该列能否写进 `fields` 数组被返回。全部非 `@internal` 列都可选中                                                                                                 |
| **谓词** (predicate)   | SQL 中返回 true / false 的表达式,即 `WHERE` 条件。本文档中守卫谓词指 `@scope` 生成的权限条件                                                                           |
| **seek 谓词**          | 游标分页中用 `>` / `<` 比较排序值的范围谓词,定位"游标行之后"的起点。区别于 offset 跳过                                                                                      |
| **tiebreaker**       | 排序中保证全序的辅助列。当主排序列存在相同值时,tiebreaker(通常是 `@pk` 列)决定唯一顺序,确保游标分页不丢行 / 重行                                                                        |
| **keyset 分页**        | 基于排序值(而非 `OFFSET`)的分页,即本文档的游标模式。客户端持上一页末行的排序值作 `after` 凭证                                                                                   |
| **fail-closed**      | 失败时默认拒绝方向(权限语义)。本文档中 `@scope` 的 `params.Scope` 为 nil → List / Count 报错,不退化为全量可见                                                             |
| **三态** (in\_any)     | 守卫函数返回值的三种语义:`NULL` → 放行全部、`{}`(空数组) → 拒绝全部、非空 → ANY 精确匹配                                                                                   |
| **预设** (preset)      | `@dynamic` / `@updatable` 的快捷名(`exact` / `membership` / `range` / `fuzzy`),展开为一个或多个 op;区别于显式 op 名(裸 `in` 是普通 op,只展开为 `in`) |
| **op**               | 操作符,即 `where` 中的比较操作(`eq` / `neq` / `gt` / `contains` 等)                                                                                    |
| **dataType**         | 字段的数据类型分类(`integer` / `string` / `datetime` / `uuid` / `enum` / `jsonb` / `int_array` 等),由 PG 类型映射而来,决定可用 op 与前端控件族                         |
| **生成期 fail**         | `sqlc generate` 阶段就报错(生成代码之前),而非运行期。报错定位到具体列与原因                                                                                             |
| **协议门**              | 协议层入口校验:客户端 JSON → `ParseXxxQuery` 的解析与校验路径,产生结构化 400 错误                                                                                    |
| **catalog**          | sqlc 解析的数据库元数据集合(表 / 列 / 类型 / 枚举定义)。`@scope` 函数不在 catalog,不做存在性校验                                                                           |
| **schema**           | 两种含义,按上下文区分:① PG 数据库 schema(命名空间,默认 `public`,sqlc.yaml 的 `schema` option);② 前端契约输出 `ResourceSchema`(查询能力描述,`GET /{resource}/_query-schema`) |
| **RLS**              | Row Level Security,PostgreSQL 行级安全策略。`@scope` 是应用层的等价物,不依赖 PG RLS                                                                           |
| **STABLE 函数**        | PostgreSQL 函数稳定性分类之一:同一语句内多次调用结果一致。`in_any` 谓词中 fn 出现两次(IS NULL 判别 + ANY 匹配),依赖此性质                                                          |

## 1. 语法规则

### 1.1 边界分隔

指令必须包裹在 `[ ]` 中且内容以 `@` 起始;不以 `@` 起始的方括号是纯文档文本,不参与生成。
注释只承载能力指令:label / desc / order / visibility / options / api 等 UI 元数据来自 `sqlc.yaml` 的 `codegen.options.ui`(结构见 [usage.md](./usage.md#codegen-options))。

```sql
COMMENT ON COLUMN users.email IS '用户邮箱，如 test@example.com [@dynamic: exact, membership, prefix @updatable: set]';
COMMENT ON COLUMN users.status IS '状态 [已废弃，仅展示] [@dynamic: exact, membership, sort]';
```

- 第一行:`用户邮箱，如 test@example.com` 是纯文档文本(不再作为 label);指令块声明读面与写面能力。
- 第二行:`[已废弃，仅展示]` 是纯文档文本;`sort` 是 `@dynamic` 的排序能力位。

### 1.2 解析规则

| 规则 | 说明 |
|---|---|
| 方括号配平 | 整条注释的 `[` / `]` 必须配平:未闭合 `[` → `unclosed '['`;裸 `]` → `unmatched ']'`。单引号字符串内部的方括号不参与配平(`@scope` / `@filter` 字面量可含 `[` / `]`)。生成期 fail,定位到表/列 |
| 指令名白名单 | 列级:`@dynamic` / `@internal` / `@updatable`;表级:`@default_sort` / `@pk` / `@scope` / `@filter`。未知 `@xxx` → 生成期 fail;已删除的 `@sortable` / `@hideable` / `@selectable` / `@hidden` / `@page` / `@update` / `@api` / `@label` / `@desc` / `@order` / `@options` 同样按未知指令 fail(无兼容期) |
| 指令块以 `@` 起始 | 进入指令解析;非指令块方括号是纯文档文本 |
| payload 分词 | `@name: payload` 形式,payload 可含空格(如 `created_at DESC`);遇到下一个 `@token` 结束 |
| 块内成对 `[ ]` | `@scope` 类型标记 `int[]` / `varchar[]` 的方括号被识别为成对,不误终止指令块 |
| 同列多指令块 | 按出现顺序合并;同名指令去重(等价于声明一次) |

### 1.3 物理列名/表名与 SQL 引号化

DSL 中的物理列名/表名按 catalog 实名书写(与 DDL 完全一致,含大小写)。生成器把物理名发射进 SQL 文本(SELECT 列、ORDER BY、where/SET/WHERE 片段、FROM)时做按需引号化:

- 裸安全标识符(`[a-z_][a-z0-9_]*` 且非 PG 保留字)原样发射;
- PG 保留字(order、group、user、check、like 等)与大小写敏感名等非常规名自动加双引号,产物 SQL 始终合法;
- 错误文案、指令回显与逻辑键(camelCase)不受影响,始终用裸名;
- 生成期不校验不拒绝——引号化对调用方透明,无需为保留字列名改 schema。

## 2. 列级指令

节序:§2.1 读面过滤与排序、§2.2 写面能力、§2.3 协议可见性排除。UI 元数据不在此层,见 usage.md 的 `codegen.options.ui`。

### 2.1 `@dynamic`

**语法**

```
@dynamic: <项, 项, ...>

项 ::= sort | preset | op
preset ::= exact | membership | range | fuzzy
op     ::= eq | neq | gt | gte | lt | lte | between | in | not_in
        | contains | prefix | suffix | is_null | not_null | has
```

> `preset` 的展开规则见 [§4.3 预设展开](#43-预设展开)。

**举例**

```sql
-- 混用能力位、预设与显式 op:
COMMENT ON COLUMN users.age IS '年龄 [@dynamic: exact, membership, range, sort @updatable: eq, range]';
-- age 是 INT(integer):exact→{eq,neq}、membership→{in,not_in}、
-- range→{gt,gte,lt,lte,between} 全部命中,加上 sort 能力位,最终 9 个读面 op + 可排序

-- 仅显式 op:
COMMENT ON COLUMN users.birth_date IS '生日 [@dynamic: eq, range]';

-- 仅预设:
COMMENT ON COLUMN users.role IS '角色 [@dynamic: fuzzy, sort]';

-- 显式空值判断(必须可空列):
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: is_null, not_null]';

-- jsonb 必须显式 has:
COMMENT ON COLUMN users.meta IS '元数据 [@dynamic: has]';

-- 只可排序(sort 之外无 op 项时仍应用 default_ops,默认 none 则为纯排序):
COMMENT ON COLUMN users.name IS '姓名 [@dynamic: sort]';
```

**描述**

声明该列可参与的过滤操作符与排序能力。

- **最终 op 集合 = 类型能力 ∩ DSL 声明**(交集规则见 §4.2 能力矩阵);
- **`sort`** **是能力位**:先于预设 / op 展开识别,只置 `Sortable`,不生成 Params 字段;`@dynamic: sort` 表示该列只可排序(默认 `default_ops: none` 时)。sort 之外无 op 项时仍应用 `default_ops`,不因 sort 声明丢失过滤能力;
- **预设项**展开为多个 op(展开表见 [§4.3](#43-预设展开)),与类型能力取交集,单个预设交集为空时静默裁剪;但整个 payload 无任何 op 命中(纯 `sort` 声明除外)→ 生成期 fail;
- **显式 op 项**严格校验,类型不支持 → 生成期 fail;
- **`is_null`** **/** **`not_null`** **完全独立**:不入任何预设,只能显式声明;唯一前提是列可空,声明于非空列 → 生成期 fail;
- **`membership`** 展开为 `in + not_in`;裸 `in` 是**普通 op**(只 `in`,不含 `not_in`);`has` 不是预设,需要时显式声明 op `has`;
- 声明后产生对应 `ListXxxParams` 字段与占位符生成;无 `@dynamic` 的列不参与过滤能力生成(但仍可写进 `fields` 被返回)。

**排序类型限制**:`int_array`(数组 seek 谓词无法参数化)、`jsonb`(结构化比较对前端无可解释语义)、枚举数组与 `unmapped` 列不可排序 → 生成期 fail;`numeric` 可排序(游标值以保留标度的十进制字符串编码,seek 占位符发射 `?::numeric`)。

**隐式 sortable**:`@pk` 列与 `@default_sort` 目标列直接获得排序能力,无需也不允许再声明 `@sortable`。

**未声明** **`@dynamic`** **的列**:不参与过滤能力生成(受 sqlc.yaml 的 `default_ops` 控制;`default_ops=none` 时静默,其他值则按预设策略为未标注列授予 op(`@internal` 与 unmapped 列除外),仍走类型交集规则)。

**`default_ops`** **只作用于含** **`@pk`** **的读面**:无 `@pk` 的入选表不生成读面,策略授予的过滤能力会被静默丢弃——`default_ops≠none` 且入选表无 `@pk` → 生成期 fail(出路:补 `@pk`,或 `default_ops: none` 后按列显式 `@dynamic`)。

### 2.2 `@updatable`

**语法**

```
@updatable
@updatable: set | locate
@updatable: <preset|op>, <preset|op>, ...
```

**举例**

```sql
-- 四形态:裸(auto)/ set-only / locate-only / 显式清单
COMMENT ON COLUMN users.name       IS '姓名 [@dynamic: fuzzy, exact, sort @updatable]';
COMMENT ON COLUMN users.email      IS '用户邮箱 [@dynamic: exact, membership @updatable: set]';
COMMENT ON COLUMN users.org_id     IS '组织ID [@dynamic: exact, membership @updatable: locate]';
COMMENT ON COLUMN users.age        IS '年龄 [@dynamic: exact, membership, range @updatable: eq, range]';
COMMENT ON COLUMN users.password   IS '密码 [@internal @updatable]';
```

**描述**

声明该列在动态更新面上的能力。命名对齐 PG updatable views——`@updatable` 的语义是"可进动态更新",而非列在 SQL 层的可变性(任何列都可被静态 `UPDATE` 改)。**与读面完全解耦**:`@dynamic` 管 List 的过滤 / 排序能力,`@updatable` 管更新面的 SET / WHERE 能力。

| 形态 | SET 方法 | WHERE 方法 | op 来源 |
| --- | --- | --- | --- |
| `@updatable`(裸,即 auto) | ✓ | ✓ | 类型能力矩阵(§4.2,不含 null op) |
| `@updatable: set` | ✓ | ✗ | — |
| `@updatable: locate` | ✗ | ✓ | 类型能力矩阵(不含 null op) |
| `@updatable: <preset\|op>, ...` | ✓ | ✓ | 显式清单(auto 失效,见下) |

已删除的拼写给出定向错误(不做兼容解析):`@updatable: auto`(裸形态已是 auto)、`@updatable: false`(省略指令即无写能力)、`@updatable: where`(更名为 `locate`)。

**op 语义**(与 `@dynamic` 同构):

- 预设(`exact` / `membership` / `range` / `fuzzy`)与类型能力取交集,类型不支持的项被裁剪而不报错;`has` 不是预设,需要时显式声明;
- 显式 op 严格校验:类型不支持 → 生成期 fail;`is_null` / `not_null` 仅可空列、且只能显式声明(裸 / locate 不自动授予 null op);
- 展开为空 → 生成期 fail;去重后按 canonical 序输出;
- 保留形态词(`set` / `locate`)**只能单独出现**,与 op 项混写(如 `@updatable: set, eq`)→ 生成期 fail;
- 同列重复 `@updatable` 取首条(与 `@dynamic` 一致);
- **运行期值域守卫**(生成代码内置,均为 `Kind=where` 显式拒绝):`contains` / `prefix` / `suffix` 的空串会退化为 `ILIKE '%%'`,等同无定位条件,必须拒绝;同一列的 `is_null` 与 `not_null` 互斥。

**SET 三态进签名**:`NOT NULL` 列 `SetName(v string)`(签名无 nil 通道);可空列 `SetBio(v *string)`(`nil` = 显式置 NULL,字面量取指针用 `dynq.Ptr`)。未出现在选项调用中的列保持原值。切片类型(`jsonb` / `int_array`)的 `nil` 在 Go 中可表达,`NOT NULL` 列的 Set 方法在运行期显式拒绝(`Kind=set`)。

**与 `@pk` 的关系**:

- 有 `@pk` 时,主键列的完整类型矩阵(不含 null op)自动生成 Where 定位方法,无需在主键上声明 `@updatable`;
- **主键列永不生成 Set,也不接受任何 `@updatable` 形态**:pk 上出现 `@updatable`(含 `set` / `locate` / 裸形态)→ 生成期 fail(主键不可变);
- 无 `@pk` 表没有自动定位方法,定位完全依赖 locate-capable `@updatable` 列。

**其他规则**:

- 与 `@internal` **正交**:`@internal` + `@updatable` 合法(服务端可写、协议不可见,`password` 类);Update 不读 `Hidden`,该列的 Set / Where 方法照常生成;
- 类型不可映射(§4.1 词表外)→ 生成期 fail;
- set-capable 列存在但无 locate-capable(且无 `@pk` 自动定位)→ 生成期 fail(更新面不可用);只有 locate-capable 而无 set-capable → 生成期 fail(死配置);
- `@filter` 目标列可 `@updatable`:UPDATE 的 WHERE 恒含该不变量,可用 `XxxUpdate.WithoutFilter()` 显式绕过;
- `@scope` **不参与写面**:Update 不消费 Scope,权限由读取阶段保证(先查后改;TOCTOU 见 [scope.md](./scope.md));
- **更新面开关只由 set-capable 列决定**:存在 set-capable `@updatable` 即生成,零 set-capable 不生成更新面且不报错(没有表级开关);
- **无 `@pk` 表的更新面**:只生成 header + marker + Set / Where + `UpdateXxx` + `UpdateXxxReturning`(多行),不生成 registry / 参数 / 解析 / List / Count / Schema / 游标 / `UpdateXxxReturningByPk`;入选要求 set-capable 与 locate-capable **同时满足**。出现读侧指令(`@dynamic` / `@scope` / `@default_sort`)→ 生成期 fail(`@dynamic` 沿用 missing `@pk` 文案);
- 无 `@pk` 表的零 where 守卫只保证"至少一个条件",条件可能命中多行——**只适合调用方自带唯一组合条件**的场景;
- **更新并返回(RETURNING)入口**:每张更新面表统一产出 `UpdateXxxReturning`(多行形态,返回 `([]<模型>, error)`),有 `@pk` 的表额外产出单行形态 `UpdateXxxReturningByPk`(主键必填定位,零命中 `pgx.ErrNoRows`)。返回行语义与零命中语义见 [README §动态更新面](./README.md#动态更新面typed-门);
- 更新面生成的包级符号 `<单数>Table` / `<单数>Update` / `<单数>UpdateOption` 与 sqlc 模型类型撞名时生成期 fail;
- `@filter` 不变量恒定 AND 进 UPDATE 的 WHERE;声明了 `@filter` 的表额外生成 `XxxUpdate.WithoutFilter()`(整表级绕过、幂等,不豁免零 where 守卫)。

**已知限制**:`locate` 形态不能与显式 op 组合;null op 不随裸 / `locate` 自动授予;同列重复 `@updatable` 取首条。

### 2.3 `@internal`

**语法**

```
@internal
```

(无 payload)

**举例**

```sql
COMMENT ON COLUMN users.password IS '密码 [@internal @updatable]';
COMMENT ON COLUMN customers.owner_user_id IS '归属用户ID [@internal]';
```

**描述**

全能力排除:该列不会被返回(不可写进 `fields`)、不可过滤、不可排序、不出现在 schema 输出中、不参与游标。只能通过 sqlc 的静态查询读到。

- 与 `@dynamic` 组合声明 → 生成期 fail;`@updatable` 例外——写能力与协议可见性正交,`@internal` + `@updatable` 合法;
- `@pk` 列不可 `@internal`(主键必须始终出现在返回列中,并作为游标列);
- 典型用途:权限守卫列(如 `owner_user_id`)、敏感字段(如 `password`)。

## 3. 表级指令

### 3.1 `@pk`

**语法**

```
@pk: <列>
```

**举例**

```sql
-- 标准形态(与 @default_sort / @filter 组合):
COMMENT ON TABLE users IS '用户表 [@default_sort: created_at DESC @pk: id @filter: deleted_at is_null]';

-- 最小接入:零 @dynamic 列(仅 List/Count/分页骨架,无过滤能力)
COMMENT ON TABLE audit_logs IS '审计日志 [@pk: id]';
```

**描述**

主键列(tiebreaker):所有生成读面的表必填;无 `@pk` 的纯更新面表(§2.2)例外。

- 形式:`@pk: <列名>`(单个列名,不含空格 / 斜杠);
- 目标列必须存在、`NOT NULL`、非 `@internal`、类型可映射,且不可声明 `@updatable`(主键不可变);
- 主键列必须游标可编码(tiebreaker 值嵌入分页游标;标量类型白名单:`integer` / `number` / `decimal` / `datetime` / `date` / `uuid` / `boolean` / `string` / `enum`),`jsonb` / 数组 / 不可映射类型作 `@pk` → 生成期 fail;
- 主键列**隐式 sortable**,无需列级声明;
- **最小接入形态**:单独声明 `@pk` 即纳入生成(零 `@dynamic` 列时,仅生成 List / Count / Parse / Schema 骨架,无过滤能力)。

### 3.2 `@default_sort`

**语法**

```
@default_sort: <列> <ASC|DESC>
```

**举例**

```sql
COMMENT ON TABLE users IS '用户表 [@default_sort: created_at DESC @pk: id]';
```

**描述**

查询未指定排序时的缺省回落。同名指令去重(见 §1.2),每表仅生效一次。

- 目标列必须存在、非 `@internal`、类型可排序;目标列**隐式 sortable**,无需列级声明;
- 方向必填(大小写不敏感);
- **不受运行期 `DisableSort` 影响**:default_sort 是表级不变量(生成期声明),`DisableSort` 只收窄调用点的**显式**排序能力,缺省回落依旧生效(该列仍并入投影与游标)。与 `@filter` 不受 `DisableWhere` 影响同构。

### 3.3 `@scope`

**语法**

```
@scope: <列> <op> <函数>(<参数,...>) [<列> <op> <函数>(<参数,...>) ...]
```

详见 [§5](#5-行级数据权限scope)。

**举例**

```sql
-- eq 标量守卫(租户隔离):
COMMENT ON TABLE projects IS '项目表 [@scope: tenant_id eq fn_tenant_id(user_id:varchar) @default_sort: created_at DESC @pk: id]';

-- in 集合守卫 + 字面量参数:
COMMENT ON TABLE customers IS '客户表 [@scope: owner_user_id in fn_accessible_user_ids(user_id:varchar, scopes:int[]) id in fn_permitted(''customers'', user_id:varchar) @pk: id]';

-- in_any 数组三态守卫:
COMMENT ON TABLE sys_user IS '员工表 [@scope: id in_any fn_perm_user_ids(user_id:varchar, scopes:int[]) @pk: id]';
```

**描述**

行级数据权限:多守卫 OR 组合(任一命中即可见)。语法、参数类型词表、SQL 形态、fail-closed 与硬边界见 [§5](#5-行级数据权限scope);完整机制与调用形态见 [scope.md](./scope.md)。

### 3.4 `@filter`

**语法**

```
@filter: <列> <op> [<字面量>] [<列> <op> [<字面量>] ...]
```

**举例**

```sql
-- 软删不变量(is_null 不带值)+ 非负金额(整数字面量):
COMMENT ON TABLE users IS '用户表 [@default_sort: created_at DESC @pk: id @filter: deleted_at is_null]';
COMMENT ON TABLE orders IS '订单表 [@default_sort: created_at DESC @pk: id @filter: amount_cents gte 0]';

-- 与 @scope 并存(filter 条件先于 scope 谓词发射,多条子句并列):
COMMENT ON TABLE projects IS '项目表 [@scope: tenant_id eq fn_tenant_id(user_id:varchar) @pk: id @filter: name eq ''template'']';
```

**描述**

服务端固定不变量:声明后作为恒定 WHERE 条件发射进 `buildXxxWhere` 头部(scope 谓词与业务条件之前),List 与 Count 共用,total 与 items 的过滤视图天然一致。

- **它是服务端不变量,不是给调用方的过滤参数**:协议 `where` 无法覆盖或取消它;调用方可见的过滤能力仍由 `@dynamic` 声明。纯服务端列建议保持 `@internal`——该列不会出现在前端 schema,不变量无需额外保密处理;
- op 封闭集合 `eq|gt|gte|lt|lte|is_null|not_null`;刻意排除 `neq` / `not_in`(`NOT IN` 撞 NULL 恒空,与 `@scope` 同一理由)与 `in`(它需要列表字面量形态,本指令不设);
- `is_null` / `not_null` 不带值;`eq` / 比较类恰带一个字面量(`'字符串'` / 整数),校验后写进生成的代码(经占位符传参,不拼进 SQL 文本);
- 同一列至多一条子句(一条不变量一列),重复 → 生成期 fail;
- 与列能力矩阵解耦:目标列可 `@internal`、可无 `@dynamic`、任意 PG 类型(仅存在性校验,不做类型推导);与同列 `@dynamic` 并存合法;
- `@filter` 同时作用于读面与写面:读面由 List / Count 共用恒等过滤;写面恒定 AND 进 UPDATE 的 WHERE,并额外生成 `XxxUpdate.WithoutFilter()` 显式绕过。表是否生成代码仍与 `@filter` 声明无关——入选条件是 `@dynamic` / `@pk` / set-capable `@updatable` / `options.tables`。声明本身的合法性一律校验;
- 带引号字面量内连续空格会被分词器折叠(与 `@scope` 字面量同一既有限制)。

## 4. 类型映射与能力矩阵

### 4.1 PG 类型 → dataType

| PG 类型                                                                                                   | dataType    | Go 类型                                    |
| ------------------------------------------------------------------------------------------------------- | ----------- | ---------------------------------------- |
| `int2` / `int4` / `int8` / `smallint` / `integer` / `bigint` / `serial` / `bigserial` / `smallserial` 等 | `integer`   | `int64`                                  |
| `numeric`                                                                                               | `decimal`   | `float64`(where 参数 `?::numeric` 显式 cast;游标值以十进制字符串编解码,seek 占位符 `?::numeric`) |
| `float4` / `float8` / `real` / `double precision`                                                       | `number`    | `float64`                                |
| `timestamptz` / `timestamp`(with / without time zone)                                                   | `datetime`  | `time.Time`                              |
| `date`                                                                                                  | `date`      | `time.Time`                              |
| `varchar` / `character varying` / `bpchar` / `char` / `character` / `text`                              | `string`    | `string`                                 |
| `bool` / `boolean`                                                                                      | `boolean`   | `bool`                                   |
| `uuid`                                                                                                  | `uuid`      | `string`                                 |
| `jsonb` / `json`                                                                                        | `jsonb`     | `[]byte`(两型统一;仅过滤 / 投影,不可排序——jsonb 排序为结构化比较,对前端无可解释语义;`has` 生成 `(col::jsonb) @> ?::jsonb`,json 列显式 cast 后比较,空值由生成代码的运行期守卫拒绝) |
| `int4` / `integer` 数组                                                                                   | `int_array` | `[]int64`                                |
| 其他数组类型                                                                                                  | (不支持)       | 生成期 fail(若列声明了 `@dynamic` / `@updatable`;`@dynamic` 的 null op 除外,见下方 unmapped 段) |
| 枚举类型(`CREATE TYPE ... AS ENUM`)                                                                         | `enum`      | sqlc-gen-go 枚举类型名                        |
| 枚举数组                                                                                                    | (不支持)       | 生成期 fail(若列声明了 `@dynamic` / `@updatable` 或命中 `default_ops`);纯投影放行 |
| 其他未知类型                                                                                                  | `unmapped`  | 不参与排序 / 写入与值 op 过滤,只作为普通返回列;可空列可显式声明 `is_null` / `not_null`(见下段)                   |

**`unmapped`** **列**:不可映射类型且未声明 `@dynamic` / `@updatable` 的列按纯展示列放行(可写进 `fields` 被返回,但不出现在 schema 的 `ops` / `sortable` 中)。声明了过滤 / 排序 / 写入能力但类型不可映射 → 生成期 fail。null op 例外:`is_null` / `not_null` 不受能力矩阵约束(§4.4 标注"任意"),unmapped 与其他数组类型的可空列显式声明即可参与过滤(生成 `col IS [NOT] NULL` 谓词);排序与写面仍不可用。

**`decimal`** **与** **`number`** **的分野在游标**:where 面两型同为 `float64` 参数(decimal 另加 `?::numeric` cast);游标面 decimal 的列值为 `pgtype.Numeric`,编码为保留标度的十进制字符串(`Int × 10^Exp`,NaN/±Infinity 为字符串哨兵,精度无损),seek 比较在服务端以 numeric 精确进行——`1.5` 与 `1.50` 编码串不同但等值判定等价。`number`(float4/float8)按 float64 收敛,不承诺十进制精度。

**排序能力位 `sort`** **的类型限制**:`int_array`(无法参数化 seek 谓词)、`jsonb`(排序为结构化比较,对前端无可解释语义)、枚举数组(标量形态的游标取值与 seek 均不成立)与 `unmapped` 不可排序 → 生成期 fail;`@pk` 与 `@default_sort` 的隐式排序同样受限。

### 4.2 能力矩阵(dataType × 默认可用 op)

最终 op 集合 = 下表中该 dataType 的能力集 ∩ DSL 声明。

| dataType    | 默认可用 op                                                  |
| ----------- | -------------------------------------------------------- |
| `integer`   | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `number`    | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `decimal`   | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `datetime`  | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `date`      | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `string`    | `eq` `neq` `in` `not_in` `contains` `prefix` `suffix`    |
| `boolean`   | `eq` `neq` `in` `not_in`                                 |
| `uuid`      | `eq` `neq` `in` `not_in`                                 |
| `enum`      | `eq` `neq` `in` `not_in`                                 |
| `jsonb`     | `has`                                                    |
| `int_array` | `in`(语义为 `&&` 数组重叠,非 `IN`)                               |

**null op 正交**:`is_null` / `not_null` 不在上表中,完全独立:不入预设、不因列可空自动获得、不从 `eq` / `neq` 推导;唯一前提是列可空,声明方式只能是显式 op。

### 4.3 预设展开

`@dynamic` 项中的预设名展开为多个 op,与类型能力取交集(交集为空时该预设对该类型不产生 op,但不报错)。

| 预设           | 展开为                             |
| ------------ | ------------------------------- |
| `exact`      | `eq` `neq`                      |
| `membership` | `in` `not_in`                   |
| `range`      | `gt` `gte` `lt` `lte` `between` |
| `fuzzy`      | `contains` `prefix` `suffix`    |

`@dynamic` 与 `@updatable` 共用同一预设表。裸 `in` 是普通 op(只 `in`,不含 `not_in`);`has` 不是预设,jsonb 列显式写 op `has`。刻意不设 `is_null` / `not_null` 预设:空值判断必须逐列显式声明。预设名 `fuzzy` 与 op `contains` 显式区分,消除同名歧义。

### 4.4 操作符语义

| op                               | SQL 语义                                                                 | 适用 dataType                                                  |
| -------------------------------- | ---------------------------------------------------------------------- | ------------------------------------------------------------ |
| `eq` / `neq`                     | `=` / `<>`                                                             | integer, number, decimal, datetime, date, string, boolean, uuid, enum |
| `gt` / `gte` / `lt` / `lte`      | 对应比较运算符                                                                | integer, number, decimal, datetime, date                              |
| `between`                        | `>= AND <=`(含两端,value=左端、rightValue=右端)                                | integer, number, decimal, datetime, date                              |
| `in` / `not_in`                  | `IN` / `NOT IN`(展开占位符,1 ≤ len ≤ MaxIn)                                 | integer / number / decimal / datetime / date / string / boolean / uuid / enum;int\_array 仅 `in`(语义为 `&&` 重叠) |
| `contains` / `prefix` / `suffix` | `ILIKE '%v%'` / `'v%'` / `'%v'`(value 中 `%` `_` `\` 全量转义,通配符位置由 op 决定) | string                                                       |
| `is_null` / `not_null`           | `IS NULL` / `IS NOT NULL`(禁止携带 value)                                  | 任意,仅可空列且显式声明                                                 |
| `has`                            | `(col::jsonb) @> ?::jsonb`(json 包含;左列显式 cast 使 `json` 与 `jsonb` 同形态;空值显式拒绝)                                  | jsonb                                                        |

## 5. 行级数据权限(`@scope`)

表级指令 `@scope` 为动态表声明行级数据权限。多守卫恒为 **OR 组合**(任一命中即可见)。完整机制、调用形态与 fail-closed 边界见 [scope.md](./scope.md);本节聚焦 DSL 语法。

### 5.1 语法

```
@scope: <列> <op> <函数>(<参数,...>) [<列> <op> <函数>(<参数,...>) ...]
```

- **列**:snake\_case 物理列名,必须存在;同列仅允许一条守卫(重复 → 生成期 fail)。列可 `@internal`(权限列典型形态)、可无 `@dynamic`、可是任意 PG 类型(类型契约由守卫函数返回形态保证)。
- **op**:封闭集合 `in` / `in_any` / `eq` / `gt` / `gte` / `lt` / `lte`(刻意排除 `neq` / `not_in`——`NOT IN` 撞上集合含 NULL 时恒空,是权限事故配方)。
- **函数**:小写标识符,可带一层 schema 限定(如 `auth.fn_xxx`);本体不在 sqlc catalog,不做存在性校验(与 options.ui api endpoint 同一信任级别),由部署侧保证。函数名拼进 SQL 字面量故严格校验,杜绝引号 / 分号 / 括号等注入面。
- **参数**:零个或多个,标识符参数 `name:type` 或字面量参数 `'字符串'` / 整数。

### 5.2 op 与 SQL 形态

| op                          | SQL 形态                                    | fn 返回                                                                                                                            |
| --------------------------- | ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `in`                        | `col IN (SELECT * FROM fn(...))`          | 单列集合,类型与目标列一致;fail-closed(NULL 行 = 拒绝)                                                                                           |
| `in_any`                    | `(fn(...) IS NULL OR col = ANY(fn(...)))` | 数组(类型与目标列一致);三态:NULL=放行全部、`{}`=拒绝全部、非空=ANY 精确匹配。**注意 NULL 放行方向与** **`in`** **相反**——服务于"NULL 表示全租户"一类显式放行契约,守卫函数须保证 NULL 只由显式分支返回 |
| `eq`                        | `col = fn(...)`                           | 标量;NULL 列对比较恒为假(不可见,fail-closed)                                                                                                 |
| `gt` / `gte` / `lt` / `lte` | `col > fn(...)` 等(时间窗权限)                  | 标量                                                                                                                               |

**`in_any`** **的 fn 必须返回数组而非集合返回函数**:集合形态下"空集"与"NULL 行"不可区分(`fn() IS NULL` 两者同为真),三态无法表达;数组形态 NULL(数组本身)与 `{}` 可区分。

### 5.3 参数类型词表

标识符参数 `name:type` 的类型标记为封闭集合,DSL 标记 → Go 类型(汇入该表的 `XxxScope` 结构体):

| DSL 标记      | Go 类型             | 用途                 |
| ----------- | ----------------- | ------------------ |
| `varchar`   | `string`          | 标量字符串(如 `user_id`) |
| `int`       | `int64`           | 标量整数(PG 整数全量承载)    |
| `bool`      | `bool`            | 标量布尔               |
| `varchar[]` | `[]string`        | 字符串集合(多角色并集)       |
| `int[]`     | `[]int64`         | 整数集合(如 `scopes`)   |
| `jsonb`     | `json.RawMessage` | 任意 JSON 标量         |

词表外类型 → 生成期 fail。字面量参数 `'字符串'` / 整数经分词器校验后内联进 SQL(不进 Scope 结构体);字符串字面量内部禁引号、不支持转义、连续空格会被指令分词折叠,应避免连续空格。

### 5.4 fail-closed 与硬边界

- 声明 `@scope` 的表,`params.Scope` 为 nil 时 List / Count 直接报错(`Kind=scope`),不静默退化为全量可见;
- 协议门结构性隔离:客户端 JSON 不存在设置 Scope 的通道,权限值只能来自服务端注入;
- 一列一守卫(重复 → fail);
- 参数不做行相关(不允许引用本行其他列——相关子查询退化为逐行求值);
- 同名参数类型冲突、不同参数名折叠同一 Go 字段名 → 生成期 fail(权限载体字段歧义不允许存在);
- 需 AND 复合逻辑(租户 AND 归属)时写进函数内部或交给 RLS 层,DSL 保持扁平 OR。

## 6. 校验规则与 fail 条件汇总

| 条件 | 结果 |
| --- | --- |
| 注释方括号未配平(未闭合 `[` 或裸 `]`) | 生成期 fail,定位到表/列(单引号字符串内部除外) |
| 指令块内未知 `@xxx` 标签(含已删除指令与旧名) | 生成期 fail,定位到列与标签名 |
| `@internal` 与 `@dynamic` 组合 | 生成期 fail(`@updatable` 除外:写能力与协议可见性正交) |
| `@internal` 带 payload | 生成期 fail |
| `@updatable` 保留形态词与 op 项混写(如 `set, eq`) | 生成期 fail |
| `@updatable` 已删除拼写 `auto` / `false` / `where` | 生成期 fail(定向提示裸形态 / 省略 / `locate`) |
| `@updatable` 显式 op 项既非预设也非已知 op | 生成期 fail |
| `@updatable` 显式 op 项类型不支持(如对 `enum` 声明 `gt`) | 生成期 fail |
| `@updatable` 显式 op 项为 `is_null` / `not_null` 且列非空 | 生成期 fail |
| `@updatable` 显式清单展开后无任何 op 命中 | 生成期 fail |
| `@pk` 列声明任意 `@updatable` 形态 | 生成期 fail(主键不可变) |
| `@updatable` 列类型不可映射或枚举数组 | 生成期 fail |
| set-capable 而无 locate-capable(且无 `@pk` 自动定位) | 生成期 fail(更新面不可用) |
| locate-capable 声明而无 set-capable | 生成期 fail(死配置) |
| 无 `@pk` 表出现读侧指令(`@dynamic` / `@scope` / `@default_sort`) | 生成期 fail(复用 missing `@pk` 文案) |
| 更新面符号 `<单数>Table` / `<单数>Update` / `<单数>UpdateOption` 与模型类型撞名 | 生成期 fail(同包符号冲突,重命名冲突表) |
| `@dynamic` 项既非 `sort` / 预设也非已知 op | 生成期 fail |
| `@dynamic` 显式 op 项类型不支持(如对 `bool` 声明 `range`) | 生成期 fail |
| `@dynamic` 显式 op 项为 `is_null` / `not_null` 且列非空 | 生成期 fail |
| `@dynamic` 项展开后无任何 op 命中(含整个 payload 只有预设且全部被裁剪) | 生成期 fail(纯 `sort` 声明除外) |
| `sort` 声明在 `jsonb` / `int_array` / 枚举数组 / `unmapped` 列(含隐式 sortable) | 生成期 fail |
| `options.default_ops` 取值不在 `none|exact|membership|range|fuzzy` | 生成期 fail |
| `options.page` 区间非法(`default`/`max` < 1 或 `default > max`) | 生成期 fail |
| `options.page.tables` 指向不存在或未入选的表 | 生成期 fail(死配置) |
| `options.ui` 表不存在 / 未入选 / 无读面 | 生成期 fail(死配置) |
| `options.ui` 列不存在 / 该列 `@internal` / 空条目 | 生成期 fail |
| `options.ui` 未知键(严格解码) | 生成期 fail |
| `options.ui.visibility` 取值不在 `always|optional|hidden` | 生成期 fail |
| `options.ui` 的 `options` 与 `api` 同列组合 | 生成期 fail(一个字段只有一种选项来源) |
| `options.ui.options` 为空列表 / 类型非 `integer` / `uuid` / `string` / value 类型不符 / 重复 / label 为空 | 生成期 fail |
| `options.ui.api` 类型非 `integer` / `uuid` / `string` / 缺 `value_key`/`label_key` / `endpoint` 含 `?` 或 `=` / `mode` 非法 | 生成期 fail |
| 无 `@pk` 且出现读侧指令(或经 `options.tables` 点名却既无读面也无更新面) | 生成期 fail(无 `@pk` 的纯更新面合法:set-capable + locate-capable 同时满足) |
| `@pk` 指向不存在 / 可空 / `@internal` / 游标不可编码类型(`jsonb` / 数组 / 不可映射)的列 | 生成期 fail |
| `@default_sort` 目标不存在 / `@internal` / 不可映射 / 不可排序 | 生成期 fail |
| `@scope` 重复守卫同列 / op 不在封闭集合 / 参数类型词表外 / 同名参数类型冲突 / Go 字段折叠 / 函数名非法 | 生成期 fail |
| `@filter` op 不在封闭集合 / `is_null` 带值 / 缺字面量 / 同列重复 / 目标列不存在 | 生成期 fail |
| camelCase 折叠冲突(两列归一为同一逻辑键) | 生成期 fail |
| 生成范围为空(无 `@dynamic` / `@pk` / set-capable `@updatable` 声明且未在 `options.tables` 中列出) | 生成期 fail |

## 7. 返回列控制真值表

**能力标记** = 给列**添加**能力的指令:`@dynamic`(过滤,或 `sort` 能力位)。与之相对,`@internal` 是**排除**标记(移除全部协议能力)。`@updatable` 只作用于写面,不改变读面。

| 列标记 / manifest | 默认返回 | `fields` 可选中 | where 过滤 | sort 排序 | 前端初始显隐 |
| --- | --- | --- | --- | --- | --- |
| 无标记 | ✓ | ✓ | ✗ | ✗ | optional |
| `@dynamic: <op...>` | ✓ | ✓ | ✓(声明 op 集) | ✗ | optional |
| `@dynamic: sort` | ✓ | ✓ | 按 default_ops(默认 none 时 ✗) | ✓ | optional |
| `@pk` / `@default_sort` 目标列 | ✓ | ✓ | ✗ | ✓(隐式) | optional |
| `options.ui.visibility: always` | ✓ | ✓ | 由能力决定 | 由能力决定 | 固定显示 |
| `options.ui.visibility: optional`(缺省) | ✓ | ✓ | 由能力决定 | 由能力决定 | 默认显示、可切换 |
| `options.ui.visibility: hidden` | ✓ | ✓ | 由能力决定 | 由能力决定 | 默认收起、可展开 |
| `@internal` | ✗ 永不返回 | ✗ | ✗ | ✗ | 不进入 schema |

- `visibility` 只决定前端列表初始显隐,不参与服务端校验,也不改变默认投影(`fields: nil` 仍是全部非 `@internal` 列);
- `hidden` 列仍会被默认查询返回,前端只是不渲染;
- `@internal` 与 `visibility` 不同:`@internal` 是协议面永不可见(不进入 schema / `fields`),`visibility` 只作用于已进入前端的列;
- `@internal` + `@updatable` 合法——服务端可写、协议不可见(如 `password`);Update 不读 `Hidden`。

## 8. 完整示例

```sql
CREATE TYPE user_status AS ENUM ('active', 'inactive', 'banned');
CREATE TYPE order_status AS ENUM ('pending', 'paid', 'shipped', 'closed');

CREATE TABLE users (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT        NOT NULL,
    email       TEXT        NOT NULL UNIQUE,
    password    TEXT        NOT NULL,
    status      user_status NOT NULL DEFAULT 'active',
    role        TEXT,
    age         INT,
    score       DOUBLE PRECISION,
    balance     NUMERIC(12, 2),
    birth_date  DATE,
    org_id      UUID,
    tags        INT[],
    meta        JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ,
    deleted_at  TIMESTAMPTZ
);

COMMENT ON COLUMN users.id         IS 'ID [@dynamic: exact, membership]';
COMMENT ON COLUMN users.name       IS '姓名 [@dynamic: fuzzy, exact, sort @updatable]';
COMMENT ON COLUMN users.email      IS '用户邮箱，如 test@example.com [@dynamic: exact, membership, prefix @updatable: set]';
COMMENT ON COLUMN users.password   IS '密码 [@internal @updatable]';
COMMENT ON COLUMN users.status     IS '状态 [已废弃，仅展示] [@dynamic: exact, membership, sort @updatable]';
COMMENT ON COLUMN users.role       IS '角色 [@dynamic: fuzzy, sort @updatable: exact]';
COMMENT ON COLUMN users.age        IS '年龄 [@dynamic: exact, membership, range, sort @updatable: eq, range]';
COMMENT ON COLUMN users.score      IS '评分 [0-100] [@dynamic: range, sort @updatable: set]';
COMMENT ON COLUMN users.balance    IS '余额 [元] [@dynamic: range, sort @updatable: set]';
COMMENT ON COLUMN users.birth_date IS '生日 [@dynamic: eq, range]';
COMMENT ON COLUMN users.org_id     IS '组织ID [@dynamic: exact, membership @updatable: locate]';
COMMENT ON COLUMN users.tags       IS '标签 [@dynamic: membership @updatable]';
COMMENT ON COLUMN users.meta       IS '元数据 [@dynamic: has @updatable: set]';
COMMENT ON COLUMN users.created_at IS '创建时间 [@dynamic: exact, membership, range, sort]';
COMMENT ON COLUMN users.updated_at IS '更新时间 [@dynamic: range, is_null, not_null, sort @updatable: eq, is_null]';
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: is_null, not_null @updatable: set]';
COMMENT ON TABLE  users            IS '用户表 [@default_sort: created_at DESC @pk: id @filter: deleted_at is_null]';

-- options.ui 的 api:引用字段选项数据源(不在 DDL 注释中声明)
CREATE TABLE orders (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_no     TEXT        NOT NULL,
    user_id      BIGINT      NOT NULL,
    amount_cents BIGINT      NOT NULL,
    status       order_status NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN orders.user_id IS '用户ID [@dynamic: eq, membership @updatable: locate]';
COMMENT ON COLUMN orders.status IS '状态 [@dynamic: exact, membership, sort @updatable: eq, neq]';
COMMENT ON COLUMN orders.amount_cents IS '金额（分） [@dynamic: range, sort @updatable: range]';
COMMENT ON TABLE  orders          IS '订单表 [@default_sort: created_at DESC @pk: id]';

-- @scope 三守卫 OR:归属用户集合 / 归属部门集合 / 显式共享字面量参数
CREATE TABLE customers (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL,
    amount_cents  BIGINT      NOT NULL DEFAULT 0,
    owner_user_id varchar(64) NOT NULL,
    owner_dept_id varchar(64) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

COMMENT ON COLUMN customers.id   IS 'ID [@dynamic: exact]';
COMMENT ON COLUMN customers.name IS '客户名 [@dynamic: fuzzy, sort @updatable: set]';
COMMENT ON COLUMN customers.amount_cents IS '金额（分） [@dynamic: range, sort @updatable: set]';
COMMENT ON COLUMN customers.owner_user_id IS '归属用户ID [@internal]';
COMMENT ON COLUMN customers.owner_dept_id IS '归属部门ID [@internal]';
COMMENT ON TABLE  customers IS '客户表 [@scope: owner_user_id in fn_accessible_user_ids(user_id:varchar, scopes:int[]) owner_dept_id in fn_accessible_dept_ids(user_id:varchar, scopes:int[]) id in fn_permitted(''customers'', user_id:varchar) @default_sort: created_at DESC @pk: id]';

-- @scope eq 标量守卫:租户隔离(tenant_id = fn_tenant_id(user_id))
CREATE TABLE projects (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL,
    tenant_id     varchar(64) NOT NULL,
    owner_user_id varchar(64) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN projects.name IS '项目名 [@dynamic: fuzzy, sort @updatable]';
COMMENT ON COLUMN projects.tenant_id IS '租户ID [@internal]';
COMMENT ON TABLE  projects IS '项目表 [@scope: tenant_id eq fn_tenant_id(user_id:varchar) @default_sort: created_at DESC @pk: id @filter: name eq ''template'']';

-- @scope in_any 数组三态守卫(NULL=全租户放行 / {}=拒绝 / 非空=ANY 精确匹配)
CREATE TABLE sys_user (
    id            varchar(64) PRIMARY KEY,
    name          varchar(64) NOT NULL,
    owner_dept_id varchar(64) NOT NULL,
    tenant_id     varchar(64) NOT NULL,
    deleted_at    TIMESTAMPTZ
);

COMMENT ON COLUMN sys_user.id   IS 'ID [@dynamic: exact]';
COMMENT ON COLUMN sys_user.name IS '姓名 [@dynamic: fuzzy, sort @updatable]';
COMMENT ON TABLE sys_user IS '员工表 [@scope: id in_any fn_perm_user_ids(user_id:varchar, scopes:int[]) @pk: id]';

-- 无 @pk 表:只生成更新面(键列 locate-capable 复合定位、note set-capable)
CREATE TABLE sys_resource_grant (
    resource  varchar(64) NOT NULL,
    user_id   varchar(64) NOT NULL,
    record_id bigint      NOT NULL,
    note      TEXT,
    PRIMARY KEY (resource, user_id, record_id)
);

COMMENT ON COLUMN sys_resource_grant.resource  IS '资源 [@updatable: locate]';
COMMENT ON COLUMN sys_resource_grant.user_id   IS '用户ID [@updatable: locate]';
COMMENT ON COLUMN sys_resource_grant.record_id IS '记录ID [@updatable: locate]';
COMMENT ON COLUMN sys_resource_grant.note      IS '备注 [@updatable: set]';

-- @pk 最小接入形态:零 @dynamic 列(仅 List/Count/分页骨架)
CREATE TABLE audit_logs (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id   BIGINT      NOT NULL,
    action     TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE audit_logs IS '审计日志 [@pk: id]';
```

守卫函数实现(`fn_accessible_user_ids` / `fn_tenant_id` / `fn_perm_user_ids` 等)见 [example/db/schema.sql](./example/db/schema.sql)。
