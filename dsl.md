# 注释 DSL 参考

sqlc_gen_dynamic 的全部生成能力声明在 DDL 注释中,随迁移版本化。本文档是 DSL 的完整参考:语法规则、列级/表级指令、类型映射、能力矩阵、预设展开与校验规则。

- 查询入参契约、响应信封 → 见 [README](./README.md);前端 Schema 契约 → 见 [schema.md](./schema.md);行级数据权限机制 → 见 [scope.md](./scope.md)
- 外部工程接入(sqlc.yaml 接线、options) → 见 [usage.md](./usage.md)
- 设计原理与边界论证 → 见 [desc.md](./desc.md)
- 端到端示例 → 见 [example/db/schema.sql](./example/db/schema.sql)

## 0. 术语表

| 术语 | 含义 |
|---|---|
| **`fields` 列选择** | 请求参数 `fields` 指定本次返回哪些列(等价于 SQL `SELECT` 的列列表)。不传 = 返回全部非 `@hidden` 列;显式传空数组 `[]` 被拒绝;未选中的列在结果里是零值,与 NULL / 真零值不可区分 |
| **`fields` 可选中** | 该列能否写进 `fields` 数组被返回。全部非 `@hidden` 列都可选中 |
| **谓词** (predicate) | SQL 中返回 true / false 的表达式,即 `WHERE` 条件。本文档中守卫谓词指 `@scope` 生成的权限条件 |
| **seek 谓词** | 游标分页中用 `>` / `<` 比较排序值的范围谓词,定位"游标行之后"的起点。区别于 offset 跳过 |
| **tiebreaker** | 排序中保证全序的辅助列。当主排序列存在相同值时,tiebreaker(通常是 `@pk` 列)决定唯一顺序,确保游标分页不丢行 / 重行 |
| **keyset 分页** | 基于排序值(而非 `OFFSET`)的分页,即本文档的游标模式。客户端持上一页末行的排序值作 `after` 凭证 |
| **fail-closed** | 失败时默认拒绝方向(权限语义)。本文档中 `@scope` 的 `params.Scope` 为 nil → List / Count 报错,不退化为全量可见 |
| **三态** (in_any) | 守卫函数返回值的三种语义:`NULL` → 放行全部、`{}`(空数组) → 拒绝全部、非空 → ANY 精确匹配 |
| **预设** (preset) | `@dynamic` 的快捷名(`exact` / `in` / `range` / `fuzzy` / `has`),展开为多个 op;区别于显式 op 名 |
| **op** | 操作符,即 `where` 中的比较操作(`eq` / `neq` / `gt` / `contains` 等) |
| **dataType** | 字段的数据类型分类(`integer` / `string` / `datetime` / `uuid` / `enum` / `jsonb` / `int_array` 等),由 PG 类型映射而来,决定可用 op 与前端控件族 |
| **生成期 fail** | `sqlc generate` 阶段就报错(生成代码之前),而非运行期。报错定位到具体列与原因 |
| **协议门** | 协议层入口校验:客户端 JSON → `ParseXxxQuery` 的解析与校验路径,产生结构化 400 错误 |
| **catalog** | sqlc 解析的数据库元数据集合(表 / 列 / 类型 / 枚举定义)。`@scope` 函数不在 catalog,不做存在性校验 |
| **schema** | 两种含义,按上下文区分:① PG 数据库 schema(命名空间,默认 `public`,sqlc.yaml 的 `schema` option);② 前端契约输出 `ResourceSchema`(查询能力描述,`GET /{resource}/_query-schema`) |
| **RLS** | Row Level Security,PostgreSQL 行级安全策略。`@scope` 是应用层的等价物,不依赖 PG RLS |
| **STABLE 函数** | PostgreSQL 函数稳定性分类之一:同一语句内多次调用结果一致。`in_any` 谓词中 fn 出现两次(IS NULL 判别 + ANY 匹配),依赖此性质 |

## 1. 语法规则

### 1.1 边界分隔

指令必须包裹在 `[ ]` 中且内容以 `@` 起始;不以 `@` 起始的方括号是 label 字面文本(人类注释方括号不误伤)。

```sql
COMMENT ON COLUMN users.email IS '用户邮箱，如 test@example.com [@dynamic: exact, prefix @selectable]';
COMMENT ON COLUMN users.status IS '状态 [已废弃，仅展示] [@dynamic: exact, in @sortable]';
```

- 第一行:`[@dynamic: ... @selectable]` 以 `@` 起始 → 指令块;`用户邮箱，如 test@example.com` 是 label。
- 第二行:`[已废弃，仅展示]` 不以 `@` 起始 → label 字面文本(方括号原样保留);`[@dynamic: ... @sortable]` 是指令块。

### 1.2 解析规则

| 规则 | 说明 |
|---|---|
| 未闭合的 `[` | 生成期 fail,定位到列 |
| 指令块以 `@` 起始 | 进入指令解析 |
| 非指令块方括号 | 原样保留为 label |
| 同列多指令块 | 按出现顺序合并;同名指令去重(等价于声明一次) |
| 指令名白名单 | 列级:`@dynamic` / `@sortable` / `@hidden` / `@selectable` / `@api`;表级:`@default_sort` / `@page` / `@pk` / `@scope`。未知 `@xxx`(如 `@sortble` 手误) → 生成期 fail |
| 指令块内 payload | `@name: payload` 形式,payload 可含空格(如 `created_at DESC`);遇到下一个 `@token` 结束 |
| 块内成对 `[ ]` 不终止块 | `@scope` 类型标记 `int[]` / `varchar[]` 的方括号被识别为成对,不误终止指令块 |

## 2. 列级指令

### 2.1 `@dynamic`

**语法**

```
@dynamic: <项, 项, ...>

项 ::= preset | op
preset ::= exact | in | range | fuzzy | has
op     ::= eq | neq | gt | gte | lt | lte | between | in | not_in
        | contains | prefix | suffix | is_null | not_null | has
```

> `preset` 的展开规则见 [§4.3 预设展开](#43-预设展开)。

**举例**

```sql
-- 混用预设与显式 op:
COMMENT ON COLUMN users.age IS '年龄 [@dynamic: exact, in, range @sortable @selectable]';
-- age 是 INT(integer):exact→{eq,neq}、in→{in,not_in}、range→{gt,gte,lt,lte,between} 全部命中能力,最终 9 个 op

-- 仅显式 op:
COMMENT ON COLUMN users.birth_date IS '生日 [@dynamic: eq, range @selectable]';
-- birth_date 是 DATE:date 能力集与 integer 一致,range 全集命中,最终 6 个 op(eq + range 5 个)

-- 仅预设:
COMMENT ON COLUMN users.role IS '角色 [@dynamic: fuzzy @sortable]';
-- role 是 TEXT(string):fuzzy→{contains,prefix,suffix} 全部命中,最终 3 个 op

-- 显式空值判断(必须可空列):
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: is_null, not_null]';

-- jsonb 必须显式 has:
COMMENT ON COLUMN users.meta IS '元数据 [@dynamic: has]';

-- 预设与显式 op 混用 + null op 共存:
COMMENT ON COLUMN users.updated_at IS '更新时间 [@dynamic: range, is_null, not_null @sortable]';
```

**描述**

声明该列可参与的过滤操作符。项可为预设名或显式 op 名,二者可混用,以逗号分隔。

- **最终 op 集合 = 类型能力 ∩ DSL 声明**(交集规则见 §4.2 能力矩阵);
- **预设项**展开为多个 op(展开表见 [§4.3](#43-预设展开)),与类型能力取交集,交集为空的项被静默裁剪(不报错);
- **显式 op 项**严格校验,类型不支持 → 生成期 fail;
- **`is_null` / `not_null` 完全独立**:不入任何预设,只能显式声明;唯一前提是列可空,声明于非空列 → 生成期 fail;
- 声明后产生对应 `ListXxxParams` 字段与占位符生成;无 `@dynamic` 的列不参与过滤能力生成(但仍可写进 `fields` 被返回)。

**未声明 `@dynamic` 的列**:不参与过滤能力生成(受 sqlc.yaml 的 `default_ops` 控制;`default_ops=none` 时静默,其他值则按预设策略为未标注列授予 op,仍走类型交集规则)。

### 2.2 `@sortable`

**语法**

```
@sortable
```

(无 payload)

**举例**

```sql
COMMENT ON COLUMN users.created_at IS '创建时间 [@dynamic: exact, in, range @sortable @selectable]';
```

**描述**

声明该列可参与 ORDER BY。

- 类型限制:`numeric` 列(pgtype.Numeric 无法编码游标值)、`int_array` 列(数组 seek 谓词无法参数化)、`unmapped` 列不可声明 → 生成期 fail;
- 与 `@default_sort` 联动:表级 `@default_sort` 目标列必须已声明 `@sortable`;
- 与 `@hidden` 互斥(组合 → fail)。

### 2.3 `@selectable`

**语法**

```
@selectable
```

(无 payload)

**举例**

```sql
COMMENT ON COLUMN users.name IS '姓名 [@dynamic: fuzzy, exact @sortable @selectable]';
```

**描述**

纯 UI 提示:该列前端表格列头可渲染与显隐交互。

**不参与服务端任何校验路径**——服务端允许写进 `fields` 的列 = 全部非 `@hidden` 列,与 `@selectable` 无关。仅流入前端 schema 决定列头渲染。

### 2.4 `@hidden`

**语法**

```
@hidden
```

(无 payload)

**举例**

```sql
-- 敏感字段:
COMMENT ON COLUMN users.password IS '密码 [@hidden]';

-- 权限守卫列:
COMMENT ON COLUMN customers.owner_user_id IS '归属用户ID [@hidden]';
```

**描述**

全能力排除:该列不会被返回(不可写进 `fields`)、不可过滤、不可排序、不出现在 schema 输出中、不参与游标。只能通过 sqlc 的静态查询读到。

- 与 `@dynamic` / `@sortable` / `@selectable` / `@api` 任一组合声明 → 生成期 fail;
- `@pk` 列不可 `@hidden`(主键必须始终出现在返回列中,并作为游标列);
- 典型用途:权限守卫列(如 `owner_user_id`)、敏感字段(如 `password`)。

### 2.5 `@api`

**语法**

```
@api: <endpoint> value_key=<字段名> label_key=<字段名> [mode=<single|multi|search>]
```

**举例**

```sql
-- mode 缺省,由 ops 推导(含 in → multi):
COMMENT ON COLUMN orders.user_id IS '用户ID [@dynamic: eq, in @selectable @api: /api/v1/users/options value_key=id label_key=name]';

-- mode 显式声明:
COMMENT ON COLUMN orders.user_id IS '用户ID [@api: /api/v1/users/options value_key=id label_key=name mode=multi]';
```

**描述**

声明该引用字段的 where 值控件 options 来自远程接口(引用型 ID 选择器,如 `user_id` / `assignee_id`)。仅 `integer` / `uuid` / `string` 列可声明。

| 参数 | 必填 | 说明 |
|---|---|---|
| `endpoint` | 是 | 相对前端 baseURL 的路径;禁止含 `?` 查询串(参数由前端按协议拼) |
| `value_key` | 是 | items 响应中每项的顶层字段名,值类型与列 dataType 一致 |
| `label_key` | 是 | items 响应中每项的顶层字段名,展示文本来源 |
| `mode` | 否 | `single` / `multi` / `search`;缺省由 ops 推导(含 `in` → `multi`、含 `contains`/`prefix`/`suffix` → `search`、否则 `single`) |

**mode 语义**:

| mode | 前端控件形态 | options 拉取请求参数 |
|---|---|---|
| `single` | 单选下拉(一次选 1 个值,选中值作为 where 的 `value`) | `page` / `pageSize` |
| `multi` | 多选下拉(一次选 N 个值,选中值数组作为 where 的 `value`) | `page` / `pageSize` |
| `search` | 搜索框(输入关键字 → 远程过滤 → 选中值) | `page` / `pageSize` + `q`(关键字) |

分页是选项接口的通用能力,与 mode 无关——任意 mode 均支持 `page` / `pageSize`;`search` 模式前端额外携带 `q` 关键字,由 options 接口按 `q` 过滤返回 items。

**`search` 模式详述**:

`search` 适用于 options 候选量大(无法一次拉完)的引用字段——前端按用户输入的关键字远程过滤,选中后再作为 where 的 `value` 提交。

- **触发方式**:显式声明 `mode=search`;或 ops 推导——列声明了 `contains` / `prefix` / `suffix`(即 `fuzzy` 预设)任一 op 时,缺省 `mode` 自动推导为 `search`(实现见 [dynq/schema.go inferAPIMode](./dynq/schema.go));
- **请求参数**:`q`(关键字)+ `page` / `pageSize`(分页通用);
- **响应信封**(全 mode 统一):`{"items": [{"<valueKey>": ..., "<labelKey>": ...}], "hasMore": false}`;`hasMore=true` 时前端携带 `page+1` 继续拉取;
- **后端实现二选一**:
  - **复用生成方法**:把 `q` 喂给该资源 `ListXxx` 的模糊匹配字段(如 `ListUsersParams{NameContains: q, Limit: pageSize}`),`items` = `Result.Items`(每行模型,字段名天然匹配 `valueKey` / `labelKey`);
  - **独立 options 接口**(推荐生产使用:脱敏、限字段、缓存):在 `endpoint` 实现专门的 options handler,按 `q` 过滤后只返回 `valueKey` / `labelKey` 两个字段。
- **与 `multi` 的区别**:`multi` 是一次拉取全部 options(可分页)供多选下拉;`search` 是按关键字远程过滤,每次只拉匹配项。`multi` 不携带 `q`,`search` 必携带 `q`。
- **与 `single` 的区别**:`single` 控件形态是单选下拉,options 量小可一次拉完;`search` 控件形态是搜索框,options 量大需远程过滤。

**校验**:与 `@hidden` 组合、非 `integer` / `uuid` / `string` 列、缺 `value_key` 或 `label_key`、未知参数、`endpoint` 含 `?` 或首 token 是 `key=value`、`mode` 非法 → 生成期 fail。

**`@api` 是纯前端 schema 契约**:不参与服务端校验路径(ID 值仍走原有类型收敛与参数化)。完整契约(响应信封、mode 语义、翻页、复用生成方法的字段名匹配)见 [schema.md @api 契约](./schema.md#api-契约引用字段选项数据源)。

## 3. 表级指令

### 3.1 `@pk`

**语法**

```
@pk: <列>
```

**举例**

```sql
-- 标准形态(与 @default_sort / @page 组合):
COMMENT ON TABLE users IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200]';

-- 最小接入:零 @dynamic 列(仅 List/Count/分页骨架,无过滤能力)
COMMENT ON TABLE audit_logs IS '审计日志 [@pk: id]';
```

**描述**

主键列(tiebreaker),所有被生成的表必填。

- 形式:`@pk: <列名>`(单个列名,不含空格 / 斜杠);
- 目标列必须存在、`NOT NULL`、非 `@hidden`、类型可映射;
- **最小接入形态**:单独声明 `@pk` 即纳入生成(零 `@dynamic` 列时,仅生成 List / Count / Parse / Schema 骨架,无过滤能力);
- 指向可空列、`@hidden` 列或不可映射类型列 → 生成期 fail。

### 3.2 `@page`

**语法**

```
@page: <默认>/<上限>
```

**举例**

```sql
COMMENT ON TABLE users IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200]';
```

**描述**

分页每页条数默认与上限。覆盖 sqlc.yaml 的 `page_default` / `page_max`。

- 默认值必须 ≤ 上限,均 ≥ 1;否则生成期 fail;
- 协议键 `pageSize` 在此范围内生效。

### 3.3 `@default_sort`

**语法**

```
@default_sort: <列> <ASC|DESC>
```

**举例**

```sql
COMMENT ON TABLE users IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200]';
```

**描述**

查询未指定排序时的缺省回落。同名指令去重(见 §1.2),每表仅生效一次。

- 目标列必须存在、已声明 `@sortable` 且非 `@hidden`;
- 方向必填(大小写不敏感)。

### 3.4 `@scope`

**语法**

```
@scope: <列> <op> <函数>(<参数,...>) [<列> <op> <函数>(<参数,...>) ...]
```

详见 [§5](#5-行级数据权限scope)。

**举例**

```sql
-- eq 标量守卫(租户隔离):
COMMENT ON TABLE projects IS '项目表 [@scope: tenant_id eq fn_tenant_id(user_id:varchar) @default_sort: created_at DESC @pk: id @page: 20/100]';

-- in 集合守卫 + 字面量参数:
COMMENT ON TABLE customers IS '客户表 [@scope: owner_user_id in fn_accessible_user_ids(user_id:varchar, scopes:int[]) id in fn_permitted(''customers'', user_id:varchar) @pk: id]';

-- in_any 数组三态守卫:
COMMENT ON TABLE sys_user IS '员工表 [@scope: id in_any fn_perm_user_ids(user_id:varchar, scopes:int[]) @pk: id]';
```

**描述**

行级数据权限:多守卫 OR 组合(任一命中即可见)。语法、参数类型词表、SQL 形态、fail-closed 与硬边界见 [§5](#5-行级数据权限scope);完整机制与调用形态见 [scope.md](./scope.md)。

## 4. 类型映射与能力矩阵

### 4.1 PG 类型 → dataType

| PG 类型 | dataType | Go 类型 |
|---|---|---|
| `int2` / `int4` / `int8` / `smallint` / `integer` / `bigint` / `serial` / `bigserial` / `smallserial` 等 | `integer` | `int64` |
| `numeric` | `number` | `float64`(参数 `?::numeric` 显式 cast) |
| `float4` / `float8` / `real` / `double precision` | `number` | `float64` |
| `timestamptz` / `timestamp`(with / without time zone) | `datetime` | `time.Time` |
| `date` | `date` | `time.Time` |
| `varchar` / `character varying` / `bpchar` / `char` / `character` / `text` | `string` | `string` |
| `bool` / `boolean` | `boolean` | `bool` |
| `uuid` | `uuid` | `string` |
| `jsonb` / `json` | `jsonb` | `[]byte` |
| `int4` / `integer` 数组 | `int_array` | `[]int64` |
| 其他数组类型 | (不支持) | 生成期 fail(若列声明了 `@dynamic` / `@sortable`) |
| 枚举类型(`CREATE TYPE ... AS ENUM`) | `enum` | sqlc-gen-go 枚举类型名 |
| 其他未知类型 | `unmapped` | 不参与过滤 / 排序,只作为普通返回列 |

**`unmapped` 列**:不可映射类型且未声明 `@dynamic` / `@sortable` 的列按纯展示列放行(可写进 `fields` 被返回,但不出现在 schema 的 `ops` / `sortable` 中)。声明了过滤 / 排序能力但类型不可映射 → 生成期 fail。

**`@sortable` 的类型限制**:`numeric`(无法编码游标值)与 `int_array`(无法参数化 seek 谓词)与 `unmapped` 不可声明 `@sortable` → 生成期 fail。

### 4.2 能力矩阵(dataType × 默认可用 op)

最终 op 集合 = 下表中该 dataType 的能力集 ∩ DSL 声明。

| dataType | 默认可用 op |
|---|---|
| `integer` | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `number` | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `datetime` | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `date` | `eq` `neq` `in` `not_in` `gt` `gte` `lt` `lte` `between` |
| `string` | `eq` `neq` `in` `not_in` `contains` `prefix` `suffix` |
| `boolean` | `eq` `neq` `in` `not_in` |
| `uuid` | `eq` `neq` `in` `not_in` |
| `enum` | `eq` `neq` `in` `not_in` |
| `jsonb` | `has` |
| `int_array` | `in`(语义为 `&&` 数组重叠,非 `IN`) |

**null op 正交**:`is_null` / `not_null` 不在上表中,完全独立:不入预设、不因列可空自动获得、不从 `eq` / `neq` 推导;唯一前提是列可空,声明方式只能是显式 op。

### 4.3 预设展开

`@dynamic` 项中的预设名展开为多个 op,与类型能力取交集(交集为空时该预设对该类型不产生 op,但不报错)。

| 预设 | 展开为 |
|---|---|
| `exact` | `eq` `neq` |
| `in` | `in` `not_in` |
| `range` | `gt` `gte` `lt` `lte` `between` |
| `fuzzy` | `contains` `prefix` `suffix` |
| `has` | `has` |

刻意不设 `is_null` / `not_null` 预设:空值判断必须逐列显式声明。预设名 `fuzzy` 与 op `contains` 显式区分,消除同名歧义。

### 4.4 操作符语义

| op | SQL 语义 | 适用 dataType |
|---|---|---|
| `eq` / `neq` | `=` / `<>` | integer, number, datetime, date, string, boolean, uuid, enum |
| `gt` / `gte` / `lt` / `lte` | 对应比较运算符 | integer, number, datetime, date |
| `between` | `>= AND <=`(含两端,value=左端、rightValue=右端) | integer, number, datetime, date |
| `in` / `not_in` | `IN` / `NOT IN`(展开占位符,1 ≤ len ≤ MaxIn) | 除 jsonb 外全部(int_array 列 `in` 语义为 `&&` 重叠) |
| `contains` / `prefix` / `suffix` | `ILIKE '%v%'` / `'v%'` / `'%v'`(value 中 `%` `_` `\` 全量转义,通配符位置由 op 决定) | string |
| `is_null` / `not_null` | `IS NULL` / `IS NOT NULL`(禁止携带 value) | 任意,仅可空列且显式声明 |
| `has` | `@>`(json 包含) | jsonb |

## 5. 行级数据权限(`@scope`)

表级指令 `@scope` 为动态表声明行级数据权限。多守卫恒为 **OR 组合**(任一命中即可见)。完整机制、调用形态与 fail-closed 边界见 [scope.md](./scope.md);本节聚焦 DSL 语法。

### 5.1 语法

```
@scope: <列> <op> <函数>(<参数,...>) [<列> <op> <函数>(<参数,...>) ...]
```

- **列**:snake_case 物理列名,必须存在;同列仅允许一条守卫(重复 → 生成期 fail)。列可 `@hidden`(权限列典型形态)、可无 `@dynamic`、可是任意 PG 类型(类型契约由守卫函数返回形态保证)。
- **op**:封闭集合 `in` / `in_any` / `eq` / `gt` / `gte` / `lt` / `lte`(刻意排除 `neq` / `not_in`——`NOT IN` 撞上集合含 NULL 时恒空,是权限事故配方)。
- **函数**:小写标识符,可带一层 schema 限定(如 `auth.fn_xxx`);本体不在 sqlc catalog,不做存在性校验(与 `@api` endpoint 同一信任级别),由部署侧保证。函数名拼进 SQL 字面量故严格校验,杜绝引号 / 分号 / 括号等注入面。
- **参数**:零个或多个,标识符参数 `name:type` 或字面量参数 `'字符串'` / 整数。

### 5.2 op 与 SQL 形态

| op | SQL 形态 | fn 返回 |
|---|---|---|
| `in` | `col IN (SELECT * FROM fn(...))` | 单列集合,类型与目标列一致;fail-closed(NULL 行 = 拒绝) |
| `in_any` | `(fn(...) IS NULL OR col = ANY(fn(...)))` | 数组(类型与目标列一致);三态:NULL=放行全部、`{}`=拒绝全部、非空=ANY 精确匹配。**注意 NULL 放行方向与 `in` 相反**——服务于"NULL 表示全租户"一类显式放行契约,守卫函数须保证 NULL 只由显式分支返回 |
| `eq` | `col = fn(...)` | 标量;NULL 列对比较恒为假(不可见,fail-closed) |
| `gt` / `gte` / `lt` / `lte` | `col > fn(...)` 等(时间窗权限) | 标量 |

**`in_any` 的 fn 必须返回数组而非集合返回函数**:集合形态下"空集"与"NULL 行"不可区分(`fn() IS NULL` 两者同为真),三态无法表达;数组形态 NULL(数组本身)与 `{}` 可区分。

### 5.3 参数类型词表

标识符参数 `name:type` 的类型标记为封闭集合,DSL 标记 → Go 类型(汇入该表的 `XxxScope` 结构体):

| DSL 标记 | Go 类型 | 用途 |
|---|---|---|
| `varchar` | `string` | 标量字符串(如 `user_id`) |
| `int` | `int64` | 标量整数(PG 整数全量承载) |
| `bool` | `bool` | 标量布尔 |
| `varchar[]` | `[]string` | 字符串集合(多角色并集) |
| `int[]` | `[]int64` | 整数集合(如 `scopes`) |
| `jsonb` | `json.RawMessage` | 任意 JSON 标量 |

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
|---|---|
| 注释 `[` 未闭合 | 生成期 fail,定位到列 |
| 指令块内未知 `@xxx` 标签 | 生成期 fail,定位到列与标签名 |
| `@hidden` 与 `@dynamic` / `@sortable` / `@selectable` / `@api` 任一组合 | 生成期 fail |
| `@dynamic` 项既非预设也非已知 op | 生成期 fail |
| `@dynamic` 显式 op 项类型不支持(如对 `bool` 声明 `range`) | 生成期 fail |
| `@dynamic` 显式 op 项为 `is_null` / `not_null` 且列非空 | 生成期 fail |
| `@dynamic` 展开后无任何 op 命中 | 生成期 fail |
| `@sortable` 声明在 `numeric` / `int_array` / `unmapped` 列 | 生成期 fail |
| `@api` 类型非 `integer` / `uuid` / `string` | 生成期 fail |
| `@api` 缺 `value_key` / `label_key` 或含未知参数或 `endpoint` 含 `?` | 生成期 fail |
| `@api` 与 `@hidden` 组合 | 生成期 fail |
| 表注释无 `@pk` | 生成期 fail |
| `@pk` 指向不存在 / 可空 / `@hidden` / 不可映射类型的列 | 生成期 fail |
| `@default_sort` 目标不存在 / 未 `@sortable` / `@hidden` / 不可映射 | 生成期 fail |
| `@page` 默认值 > 上限 或 任一 < 1 | 生成期 fail |
| `@scope` 重复守卫同列 | 生成期 fail |
| `@scope` op 不在封闭集合 | 生成期 fail |
| `@scope` 参数类型词表外 | 生成期 fail |
| `@scope` 同名参数类型冲突 或 不同参数名折叠同一 Go 字段名 | 生成期 fail |
| `@scope` 函数名非小写标识符或含非法字符 | 生成期 fail |
| camelCase 折叠冲突(两列归一为同一逻辑键) | 生成期 fail |
| 生成范围为空(无 `@dynamic` / `@pk` 声明且未在 `options.tables` 中列出) | 生成期 fail |

## 7. 返回列控制真值表

**正向标记** = 给列**添加**能力的标记:`@dynamic` / `@sortable` / `@selectable` / `@api`(均默认放行列,只增能力)。与之相对,`@hidden` 是**排除**标记(移除全部能力)。正向标记之间可自由组合、互不冲突;与 `@hidden` 组合则自相矛盾 → 生成期 fail。

| 列标记组合 | 默认返回 | `fields` 可选中 | UI 列头渲染 | where 过滤 | sort 排序 |
|---|---|---|---|---|---|
| 无标记 | ✓ | ✓ | ✗(默认不渲染列头) | ✗ | ✗ |
| `@selectable` | ✓ | ✓ | ✓(可隐藏 / 显示) | ✗ | ✗ |
| `@dynamic` | ✓ | ✓ | ✗ | ✓(声明 op 集) | ✗ |
| `@sortable` | ✓ | ✓ | ✗ | ✗ | ✓ |
| 正向标记任意组合 | ✓ | ✓ | 有 `@selectable` 才渲染 | 有 `@dynamic` 才可 | 有 `@sortable` 才可 |
| `@hidden` | ✗ 永不返回 | ✗ | 不出现在 schema | ✗ | ✗ |
| `@hidden` 与任一正向标记组合 | 生成期 fail | | | | |

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

COMMENT ON COLUMN users.id         IS 'ID [@dynamic: exact, in @sortable @selectable]';
COMMENT ON COLUMN users.name       IS '姓名 [@dynamic: fuzzy, exact @sortable @selectable]';
COMMENT ON COLUMN users.email      IS '用户邮箱，如 test@example.com [@dynamic: exact, in, prefix @selectable]';
COMMENT ON COLUMN users.password   IS '密码 [@hidden]';
COMMENT ON COLUMN users.status     IS '状态 [已废弃，仅展示] [@dynamic: exact, in @sortable @selectable]';
COMMENT ON COLUMN users.role       IS '角色 [@dynamic: fuzzy @sortable]';
COMMENT ON COLUMN users.age        IS '年龄 [@dynamic: exact, in, range @sortable @selectable]';
COMMENT ON COLUMN users.score      IS '评分 [0-100] [@dynamic: range @sortable @selectable]';
COMMENT ON COLUMN users.balance    IS '余额 [元] [@dynamic: range @selectable]';
COMMENT ON COLUMN users.birth_date IS '生日 [@dynamic: eq, range @selectable]';
COMMENT ON COLUMN users.org_id     IS '组织ID [@dynamic: exact, in]';
COMMENT ON COLUMN users.tags       IS '标签 [@dynamic: in @selectable]';
COMMENT ON COLUMN users.meta       IS '元数据 [@dynamic: has]';
COMMENT ON COLUMN users.created_at IS '创建时间 [@dynamic: exact, in, range @sortable @selectable]';
COMMENT ON COLUMN users.updated_at IS '更新时间 [@dynamic: range, is_null, not_null @sortable]';
COMMENT ON COLUMN users.deleted_at IS '删除时间 [@dynamic: is_null, not_null]';
COMMENT ON TABLE  users            IS '用户表 [@default_sort: created_at DESC @pk: id @page: 20/200]';

-- @api 引用字段选项数据源
CREATE TABLE orders (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_no     TEXT        NOT NULL,
    user_id      BIGINT      NOT NULL,
    amount_cents BIGINT      NOT NULL,
    status       order_status NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN orders.user_id IS '用户ID [@dynamic: eq, in @selectable @api: /api/v1/users/options value_key=id label_key=name]';
COMMENT ON TABLE  orders          IS '订单表 [@default_sort: created_at DESC @pk: id @page: 20/100]';

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

COMMENT ON COLUMN customers.id   IS 'ID [@dynamic: exact @sortable @selectable]';
COMMENT ON COLUMN customers.name IS '客户名 [@dynamic: fuzzy @sortable @selectable]';
COMMENT ON COLUMN customers.owner_user_id IS '归属用户ID [@hidden]';
COMMENT ON COLUMN customers.owner_dept_id IS '归属部门ID [@hidden]';
COMMENT ON TABLE  customers IS '客户表 [@scope: owner_user_id in fn_accessible_user_ids(user_id:varchar, scopes:int[]) owner_dept_id in fn_accessible_dept_ids(user_id:varchar, scopes:int[]) id in fn_permitted(''customers'', user_id:varchar) @default_sort: created_at DESC @pk: id @page: 20/100]';

-- @scope eq 标量守卫:租户隔离(tenant_id = fn_tenant_id(user_id))
CREATE TABLE projects (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL,
    tenant_id     varchar(64) NOT NULL,
    owner_user_id varchar(64) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN projects.tenant_id IS '租户ID [@hidden]';
COMMENT ON TABLE  projects IS '项目表 [@scope: tenant_id eq fn_tenant_id(user_id:varchar) @default_sort: created_at DESC @pk: id @page: 20/100]';

-- @scope in_any 数组三态守卫(NULL=全租户放行 / {}=拒绝 / 非空=ANY 精确匹配)
CREATE TABLE sys_user (
    id            varchar(64) PRIMARY KEY,
    name          varchar(64) NOT NULL,
    owner_dept_id varchar(64) NOT NULL,
    tenant_id     varchar(64) NOT NULL,
    deleted_at    TIMESTAMPTZ
);

COMMENT ON COLUMN sys_user.id   IS 'ID [@dynamic: exact]';
COMMENT ON COLUMN sys_user.name IS '姓名 [@dynamic: fuzzy]';
COMMENT ON TABLE sys_user IS '员工表 [@scope: id in_any fn_perm_user_ids(user_id:varchar, scopes:int[]) @pk: id]';

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
