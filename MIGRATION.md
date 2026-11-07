# 迁移指南:动态查询声明载体重构(破坏性,无兼容期)

本版本重构了注释 DSL、`sqlc.yaml` options、生成代码元数据与前端 schema 契约。**不设兼容期**:旧指令与旧名不会解析为等价能力,而是直接生成期 fail;旧 `sqlc.yaml` options 键、`dynq` API 字段与前端 schema 键均不做兼容映射——这是有意的 fail-closed 设计,避免「注释里的指令静默失效、列能力悄悄改变」。

- 目标版本:下一个破坏性发布(当前 `Unreleased`;按语义化版本应为 pre-1.0 的 minor bump)
- 是否自动迁移:否
- 迁移方式:按下方步骤改写 DDL 注释与 `sqlc.yaml`,然后 `sqlc generate`,按生成期 fail 逐条清理残留
- 变更清单见 [CHANGELOG](./CHANGELOG.md) 的 Unreleased 节;新语法见 [dsl.md](./dsl.md);新 options 见 [usage.md](./usage.md#codegen-options)

## 影响面

| 层面 | 破坏点 | 迁移动作 |
| --- | --- | --- |
| DDL 注释 | 旧指令不再接受:`@label` / `@desc` / `@order` / `@options` / `@api` / `@hideable` / `@selectable` 删除,`@hidden` 改名 `@internal`,`@sortable` 合并为 `@dynamic: sort`,`@page` 迁到 `options.page`,`@update` 删除;`@updatable` 形态与预设更名;方括号必须配平 | 按第 1 步改写注释 |
| `sqlc.yaml` | 删除 `page_default` / `page_max`,新增 `page`;新增 `ui`;`default_ops` 取值变化;严格解码(未知键 fail) | 按第 2 步改写 `codegen.options` |
| 生成代码 | 文件头回显、registry 字段、schema 函数随 DSL 变化 | 重新生成,不要手改生成物 |
| `dynq` 公开 API | `FieldMeta.Selectable` 删除,改为 `Visibility`;`FieldSchema` 的 `hideable` / `selectable` 删除,改为 `visibility` | 修改调用方 Go 代码与前端消费代码 |
| 前端 schema JSON | `fields` 变为展示字段集合(非 `@internal` 且未被 `SelectDisabled`),纯投影列也会出现;`label` 缺省为物理列名;`visibility` 缺省 `optional` | 复核前端列设置、label 与列显隐默认值 |
| 注释文本 | 注释里的人类文本不再参与生成 | 依赖注释文本作 label 的列需显式声明 `ui.*.label` |

> DDL 注释本身也是 schema 迁移的一部分:改写 `COMMENT ON ...` 后需要随迁移发布,否则生产库注释与 `schema.sql` 漂移。

## 第 1 步:改写 DDL 注释

### 1.1 UI 指令迁到 `options.ui`

| 旧写法 | 新写法 |
| --- | --- |
| `[@label: 电子邮箱]` | `options.ui.users.email.label: 电子邮箱` |
| `[@desc: 账号的启用状态]` | `options.ui.users.status.desc: 账号的启用状态` |
| `[@order: 10]` | `options.ui.users.status.order: 10` |
| `[@options: 0=未知 18=成年]` | `options.ui.users.age.options: [{value: 0, label: 未知}, {value: 18, label: 成年}]` |
| `[@api: /api/v1/users/options value_key=id label_key=name]` | `options.ui.orders.user_id.api: {endpoint: /api/v1/users/options, value_key: id, label_key: name}` |
| `[@hideable]` / `[@selectable]` | `options.ui.<表>.<列>.visibility: optional`(缺省即此值,可省略) |
| 需要固定显示(不可切换) | `visibility: always` |
| 需要默认收起、可在列设置展开 | `visibility: hidden` |

注意:

- 注释里的人类文本**不再成为 label**;未声明 `label` 的列回落物理列名。依赖注释文本作展示名的列必须显式写 `label`。
- v0.5 中**未标注 `@hideable`** 的列旧行为是默认不作为列展示(`hideable=false`,仅作为过滤条件可用);新契约缺省是 `optional`(默认显示、用户可切换)。要保持在列表里默认不展示,写 `visibility: hidden`;要固定显示不可切换,写 `visibility: always`。
- `@api` 与 `@options` 仍互斥;`api` 仅支持 `integer` / `uuid` / `string` 列。

### 1.2 旧名与已删除指令

| 旧写法 | 新写法 | 说明 |
| --- | --- | --- |
| `@hidden` | `@internal` | 服务端内部列:协议面永不可见,与 UI 显隐无关 |
| `@sortable` | `@dynamic: sort` | 合并为 `@dynamic` 的能力位 |
| `@default_sort` 目标列上的 `@sortable` | 删除 | 目标列已隐式 sortable |
| `@pk` 列上的 `@sortable` | 删除 | 主键列已隐式 sortable |
| 表级 `@page: <default>/<max>` | `options.page` | 见第 2 步 |
| 表级 `@update` / `@update: true|false|auto` | 删除 | 更新面只由 set-capable `@updatable` 决定 |

`@label` / `@desc` / `@order` / `@options` / `@api` / `@hideable` / `@selectable` / `@hidden` / `@sortable` / `@page` / `@update` 现在一律按未知指令生成期 fail,报错会定位到具体表/列。

### 1.3 `@updatable` 形态收敛

| 旧写法 | 新写法 |
| --- | --- |
| `@updatable: auto` | `@updatable`(裸形态即 auto) |
| `@updatable: where` | `@updatable: locate` |
| `@updatable: false` | 直接删除该 `@updatable`(没有指令 = 没有写能力) |
| `@pk` 列上的任意 `@updatable` | 删除(主键不可变,现在生成期 fail) |

保留形态:`@updatable`(SET + WHERE)、`@updatable: set`(只 SET)、`@updatable: locate`(只 WHERE)、`@updatable: <preset|op>, ...`(显式清单)。

### 1.4 预设与裸 `in`

| 旧写法 | 新写法 | 说明 |
| --- | --- | --- |
| `in`(旧预设,展开为 `in + not_in`) | 需要双向过滤:`membership`;只需单向:`in` | 预设名 `in` 删除;裸 `in` 现在只是普通 op(只 `in`),不再隐含 `not_in` |
| 预设 `has` | 显式 op `has` | `has` 预设删除,op 保留;jsonb 列写 `@dynamic: has` |

这是一次**语义缩减**:`@dynamic: in` 不会报错,只是不再隐含 `not_in`;凡是需要「包含/不包含」双向过滤的声明都必须改写为 `membership`。`@dynamic` 与 `@updatable` 共用同一预设表(`exact` / `membership` / `range` / `fuzzy`)。

### 1.5 方括号配平

整条注释的 `[` / `]` 必须配平:

- 未闭合 `[` → `unclosed '['`;
- 裸 `]` → `unmatched ']'`;
- 单引号字符串内部不参与配平,`@scope` / `@filter` 字面量可含 `[` / `]`(引号只在指令块内被识别为定界符,人类文本里的撇号不会吞掉方括号)。

人类注释里若曾随意出现半边方括号,需补全或删除。

## 第 2 步:改写 `sqlc.yaml` options

### 2.1 分页:`page_default` / `page_max` → `page`

```yaml
options:
  page:
    default: 20
    max: 200
    tables:
      orders:    {default: 20, max: 100}
      customers: {default: 20, max: 100}
```

- `default` / `max` 缺失时取内置默认 20 / 200;
- 表级覆盖优先,未声明的字段回落全局值;
- 校验 `1 ≤ default ≤ max`;`tables` 中的表必须存在且被生成范围选中,否则死配置 fail。

### 2.2 UI:`ui`

```yaml
options:
  ui:
    users:
      id:      {visibility: always}
      email:   {label: 电子邮箱, order: 10}
      status:  {label: 用户状态, desc: 账号的启用状态, order: 10}
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

键使用**物理表名 / 物理列名**(snake_case)。校验规则:

- 表必须存在、被生成范围选中且有读面(有 `@pk`);列必须存在且非 `@internal`;空条目与 null 条目 fail;未知键严格解码 fail;
- `visibility` 取值 `always` / `optional` / `hidden`,缺省 `optional`;
- `options` 的 value 按列 dataType 定型(integer → 整数,string/uuid → 字符串),空列表 / 重复 value / label 为空 / 类型不符 → fail;
- `api` 仅 `integer` / `uuid` / `string` 列;`endpoint` 禁含 `?` 与 `=`;`value_key` / `label_key` 必填;`mode` 取 `single` / `multi` / `search`,缺省由 ops 推导;
- `options` 与 `api` 同列互斥。

### 2.3 `default_ops` 取值

`none` / `exact` / `membership` / `range` / `fuzzy`。`in` 与 `has` 不再是合法取值;`default_ops` 只作用于含 `@pk` 的读面,无 `@pk` 的入选表配非 `none` 仍生成期 fail。

## 第 3 步:生成代码与 `dynq` API

### 3.1 元数据字段

| 旧 | 新 |
| --- | --- |
| `dynq.FieldMeta.Selectable bool` | `dynq.FieldMeta.Visibility Visibility` |
| `FieldSchema.Hideable bool` + `Selectable bool` | `FieldSchema.Visibility string`(`always` / `optional` / `hidden`) |

生成物行为:registry 对每个非 `@internal` 列显式发射 `Visibility` 字段;未声明时输出 `dynq.VisibilityOptional`。`@internal` 列不发射该字段。

### 3.2 `fields` 语义

`ResourceSchema.fields` 现在是**展示字段集合**:非 `@internal` 字段减去 `opts.SelectDisabled` 收窄后的集合。

- 纯投影列(op 集为空、不可排序)也会出现在 `fields`;
- `visibility: hidden` 的列仍出现在 `fields`(初始收起),默认投影不变;
- `@internal` 字段永不出现;
- `opts.SelectDisabled` 的列从 `fields` 移除;where / sort 禁用只清空 ops / sortable,不移除字段。

### 3.3 更新面

更新面是否生成只由列级 set-capable `@updatable` 决定:存在至少一列 set-capable 即生成,零 set-capable 不生成且不报错。`@update` 不再存在。

## 第 4 步:前端 schema 消费方

- 把 `hideable` / `selectable` 读取改为 `visibility`;历史语义映射:`hideable=true` ≈ `optional`(默认显示、可切换);`hideable=false`(含未标注)旧行为是默认不展示,要保留该行为用 `hidden`,要固定显示用 `always`;
- `visibility: hidden` 的列**仍会被默认查询返回**,前端只是默认不渲染;
- `fields` 可能包含 op 为空、不可排序的纯展示列,渲染逻辑需允许这种字段;
- `label` 未在 `ui` 声明时为物理列名,不再取注释人类文本。

## 第 5 步:残留扫描

在仓库内扫描旧声明(`db/` 换成你的 schema 目录):

```bash
# 已删除的列级/表级指令与旧名
grep -rnE '\[@(sortable|hideable|selectable|hidden)\b' db/
grep -rnE '\[@(label|desc|order|options|api)\s*:' db/
grep -rnE '\[@(page|update)\b' db/

# @updatable 已删除拼写
grep -rnE '@updatable\s*:\s*(auto|false|where)\b' db/

# 依赖 in+not_in 的裸 in(需人工确认是否改写 membership)
grep -rnE '@(dynamic|updatable)\s*:[^]]*\bin\b' db/

# 旧 options
grep -rn 'page_default\|page_max' db/sqlc.yaml

# 旧 schema 键 / dynq 字段
grep -rnE '\bSelectable\b|"hideable"|"selectable"' --include='*.go' --include='*.ts' --include='*.tsx' .
```

另外检查 `@pk` 目标列是否残留 `@updatable`(现在是生成期 fail):

```bash
grep -n '@pk' db/schema.sql
# 对每个 @pk 列,确认其 COMMENT ON COLUMN 中不含 @updatable
```

## 第 6 步:验证

1. `sqlc generate`(或 `make sqlc`)成功;任何残留声明都会带表/列定位的生成期 fail。
2. `go build ./...` / `go test ./...`(生成物与 dynq 同版本)。
3. 抽查生成文件头:只剩 `@dynamic` / `@internal` / `@updatable` / `@default_sort` / `@pk` / `@scope` / `@filter`,不再出现 `@page` / `@update` / `@sortable` / `@hideable`。
4. 抽查 `XxxQuerySchema()` JSON:所有非 `@internal` 列都在 `fields`;未声明 `visibility` 的列为 `optional`;`label` 为 `ui` 声明值或物理列名。
5. 运行一次列表查询,验证显式排序(`sort` 字段)、`membership` 的 `not_in`、更新面 SET / WHERE 符合预期。
6. `git diff` 评审生成物:预期变化集中在注释回显、registry `Visibility`、UI 元数据与分页值。

## 回滚

本次变更不改变数据库结构,但会改变 `COMMENT ON` 内容与生成代码:

1. 回滚本次提交并重新生成(`git revert <commit> && make sqlc`);
2. 回滚 DDL 注释迁移(把 `COMMENT ON` 改回旧文本);
3. 恢复旧版 `dynq` 依赖版本;
4. 旧版生成物与旧版前端 schema 消费逻辑一并恢复。

## FAQ

**为什么不做兼容解析?**
旧写法与新语义存在冲突(尤其裸 `in`、`@hideable`),静默兼容会再次制造「列能力悄悄改变」的路径。生成期 fail 让每一条残留声明都可见、可定位。

**`visibility` 与 `@internal` 有什么区别?**
`@internal` 是协议面永不可见(不进入 schema / `fields`);`visibility` 只决定已进入前端的列默认怎么显示,`hidden` 列仍会被默认查询返回。

**`@dynamic: sort` 会丢失过滤能力吗?**
不会。`sort` 只是能力位;sort 之外没有 op 项时仍应用 `default_ops`(默认 `none` 时就是纯排序)。

**`@pk` 列原来写 `@updatable: where` 还能保留吗?**
不能。主键列的定位方法由 `@pk` 自动生成,任何 `@updatable` 形态(含 `locate`)在 pk 上都生成期 fail。

**旧 `@api` 为什么不放 `sqlc.yaml` 之外的文件?**
WASM 插件完全沙箱化(无文件系统/网络/env),独立 sidecar 在推荐分发形态下读不到;`codegen.options` 随 `GenerateRequest.PluginOptions` 送达,process 与 wasm 两种模式都可用。
