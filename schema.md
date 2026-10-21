# 前端 Schema(UI 契约)

每张动态表生成 `UsersQuerySchema(opts ...dynq.QueryOption) dynq.ResourceSchema`,前端据此渲染通用 QueryBuilder——一次开发,全部列表页复用;端点级能力差异(禁用)对前端透明。

- 查询入参契约、响应信封 → 见 [README](./README.md)
- `@api` 指令的 DSL 语法与校验规则 → 见 [dsl.md §2.5](./dsl.md#25-api)
- 设计原理与 UI 推断 → 见 [desc.md §10](./desc.md)

- **交付端点**:`GET /{resource}/_query-schema`;`ResourceSchema.Hash()` 为内容 hash,可作 ETag 长缓存(同一 opts 切片产出字节级相同 schema)
- **能力减法**:与 Parse 完全相同的 opts 语义——where 禁用 → 该字段 `ops` 清空;sort 禁用 → `sortable=false`;select 禁用 → `hideable=false`;字段被完全禁用(或原 `@internal`)→ 从 schema 消失,QueryBuilder 无需感知"禁用"概念

## 资源级键(ResourceSchema)

| 键 | 值类型 | 含义 |
|---|---|---|
| `resource` | string | 资源名(如 `"users"`) |
| `fields` | FieldSchema[] | 字段契约列表,见下表 |
| `defaultSort` | `{ "field": string, "desc": boolean }[]` | 缺省排序回落(查询未指定排序时生效) |
| `pagination` | object | 见下方 pagination 键表 |

## 字段契约键(FieldSchema)

| 键 | 值类型 | 含义 |
|---|---|---|
| `field` | string | 逻辑字段名(camelCase) |
| `label` | string | 展示文本(注释非指令部分) |
| `dataType` | string(枚举) | `integer` / `number` / `datetime` / `date` / `string` / `boolean` / `uuid` / `enum` / `jsonb` / `int_array` |
| `nullable` | boolean | 列可空性 |
| `sortable` | boolean | 可排序 → 表格列头可挂排序 |
| `hideable` | boolean | 纯 UI 提示:列头渲染与显隐交互(见 `@hideable`,DSL 旧名 `@selectable`) |
| `selectable` | boolean | **过渡期旧键(弃用)**:`hideable` 更名前的旧键,过渡期与 `hideable` 恒同值输出;前端切换到 `hideable` 后于大版本移除,新代码不得依赖 |
| `ops` | string[] | 本调用点有效的 where 操作符全集(能力上限 = 生成期 DSL;**操作符渲染的唯一事实源**,见下方渲染规则) |
| `enum` | string[](可选) | 枚举值列表(仅 `enum` 类型,下拉组件的 options 取自此;编译期可确定时用,静态) |
| `api` | object(可选) | 选项数据源为远程接口(`@api` 声明);有 `api` 时值控件 options 经接口拉取,`enum` 不出现。见下方 @api 契约 |

> schema **不携带 `ui` 字段**:控件族由 `dataType` 唯一确定(映射表见下方渲染规则),操作符与值控件形态由 `ops` 驱动——渲染信息全部可由 `(dataType, ops, sortable, hideable)` 派生,不做第二份副本。

## pagination 键

| 键 | 值类型 | 含义 |
|---|---|---|
| `defaultPageSize` | number | pageSize 缺省值(表级 `@page` 可覆盖) |
| `maxPageSize` | number | pageSize 上限 |
| `modes` | string[] | 分页模式子集:`["offset","cursor"]` |
| `includeTotal` | boolean | 是否支持 `includeTotal=false`(total 为 null,前端隐藏"共 N 条") |

## 前端渲染规则(契约,固化于 [desc.md §10.2](./desc.md#102-前端渲染规则契约固化进文档))

前端 QueryBuilder 依据 `(dataType, ops, sortable, hideable)` 渲染,控件族由 `dataType` 唯一确定:

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

## @api 契约(引用字段选项数据源)

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
- `@api` 仅影响前端 schema 输出,不参与任何服务端校验路径(ID 值仍走原有类型收敛与参数化);DSL 校验规则与参数详情见 [dsl.md §2.5](./dsl.md)。

## 示例(`UsersQuerySchema()` 节选)

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
      "hideable": true,
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
