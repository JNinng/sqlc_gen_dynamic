# Changelog

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 新特性
- **跨表 UI 兜底(`codegen.options.ui_defaults`)**:按物理列名声明一次 UI 元数据,对每张入选读面表中同名且非 `@internal` 的列生效,`ui.<表>.<列>` 逐字段覆盖(未声明字段继承)。`options` 与 `api` 是同一选项来源槽位,表级声明其一即替换兜底来源;兜底条目未命中任何可见列(列不存在 / 未入选 / 全为 `@internal`)按死配置生成期 fail,同名不同 dataType 的表按该表实际类型校验。展开结果与逐表手写 `options.ui` 完全一致。

## [v0.6.0] - 2026-11-07

### 破坏性变更

**无兼容期**:旧指令与旧名一律生成期 fail;旧 `sqlc.yaml` options 键、`dynq` API 字段与前端 schema 键均不做兼容映射。完整迁移步骤、旧→新对照表、残留扫描命令与验证清单见 [MIGRATION.md](./MIGRATION.md)。

- **UI 元数据迁出注释**:删除列级 `@label` / `@desc` / `@order` / `@options` / `@api` 与 `@hideable` / `@selectable`,旧名 `@hidden` 改写为 `@internal`;改由 `sqlc.yaml` 的 `codegen.options.ui` 声明(物理表名 → 物理列名 → `{label, desc, order, visibility, options, api}`);注释里的人类文本不再参与生成,label 缺省回落物理列名。`@hideable` / `@selectable` 由 `visibility`(`always` / `optional` / `hidden`,缺省 `optional`)取代。
- **表级 `@page` 移除**:分页策略统一为 `options.page`(`{default, max, tables}`,表级覆盖优先),旧 `page_default` / `page_max` 选项删除。
- **表级 `@update` 移除**:更新面开关只由 set-capable `@updatable` 列决定;`@update: true/false/auto` 与全表无条件生成均不再支持。
- **`@sortable` 合并进 `@dynamic` 的 `sort` 能力位**:`[@sortable]` → `[@dynamic: sort]`;`@pk` 列与 `@default_sort` 目标列隐式 sortable,不再要求显式声明。
- **预设与 `@updatable` 形态收敛**:预设 `in` 更名 `membership`(展开仍为 `in + not_in`),删除 `has` 预设(op `has` 保留);裸 `in` 回落为普通 op(只 `in`),依赖 `in + not_in` 的声明必须改写。`@updatable` 删除 `auto` / `false` 拼写(裸形态即 auto,省略即无写能力),`where` 更名 `locate`;`@pk` 列上的任何 `@updatable` 改为生成期 fail(主键不可变)。
- **解析器 fail-closed**:整条注释的方括号必须配平(未闭合 `[` / 裸 `]` → 生成期 fail,单引号字符串内部除外);未知指令、已删除指令与旧名一律 fail;`@internal` 组合 fail 文案收敛为 `@dynamic`。
- **前端 schema 键替换**:`dynq.FieldSchema` 删除 `hideable` / `selectable`,新增 `visibility`(三态,缺省 `optional`);`dynq.FieldMeta.Selectable` 删除,改为 `Visibility`(registry 对每个非 `@internal` 列显式发射该字段,未声明时输出 `optional`);`fields` 现在是展示字段集合(非 `@internal` 且未被 `SelectDisabled`),纯投影列同样出现,`hidden` 列仍出现在 `fields` 且默认投影不变。

### 新特性
- **UI manifest(`codegen.options.ui`)与分页配置(`options.page`)**:WASM 沙箱下随 `PluginOptions` 送达,两种分发形态均可用。manifest 严格解码(未知键 fail),表/列使用物理名;表必须存在、被生成范围选中且有读面,`@internal` 列不允许 UI 条目,死配置生成期 fail;`options.default_ops` 取值同步为 `none|exact|membership|range|fuzzy`。
- **动态更新支持 RETURNING（更新并返回被改写的行）**：每张更新面表统一产出 `UpdateXxxReturning`，一条 SQL 完成 SET / WHERE / RETURNING，返回被改写的强类型整行（`([]<模型>, error)`），调用方不再更新后回查；命中几行返回几行，行序未定义，零命中返回空切片 + nil error。有 `@pk` 的表额外产出单行形态 `UpdateXxxReturningByPk(ctx, db, pk, opts...)`：主键作为必填定位参数内建到 WHERE，零命中返回 `pgx.ErrNoRows`。是否产出由生成期按现有类型信息自动判断，不新增 DDL 注解、不提供 opt-in 开关；`UpdateXxx` 的签名与语义不变。
- **引擎与驱动前置条件显式化**：产物的 `$N` 占位符、`pgx.CollectRows` 收行、RETURNING 入口与 `dynq` 运行时的 `pgtype` 依赖均绑定 PostgreSQL + `pgx/v5`。非 `postgresql` 引擎（含 engine 缺省）现在在生成期统一报错，读面与更新面同一门槛——此前纯读面表在非 PG 引擎下会静默产出跑不起来的方言。`sql_package` 不进插件协议，无法二次校验，`database/sql` 配置会在 `go build` 期失败，已在 README 环境要求写死为硬前置。

### 修复
- **registry 注释与实际变量名一致**：此前生成文件里 registry 变量的文档注释指向不存在的符号（如 `userRegistry` 而变量为 `usersRegistry`），现已与变量名一致，符合 Go doc 注释以符号名开头的约定。

## [v0.5.1] - 2026-11-04

### 修复
- **where 条件支持字符串形式的大整型主键**：此前整型列的 where 值只收 JSON number，前端按 protojson 的 int64-as-string 规范传字符串主键（>2^53，如雪花 id）被拒为 400，改传 JS number 则精度丢失、查询静默错位。现已修复——`eq`/`neq`/`in`/`not_in`/`between` 的整型值接受十进制字符串并精确解析，数字形态行为不变。

## [v0.5.0] - 2026-11-04

### 修复
- **游标翻页支持大整型主键**：此前主键值超过 2^53 的资源（如雪花 id）在游标续翻时报 `integer out of range`，游标分页不可用，现已修复——游标中的整型值改为十进制字符串编码，精确往返无精度丢失。
  旧版本签发的游标不再被接受，升级后需丢弃旧 `after`、从首页重新翻页。

## [v0.4.0] - 2026-11-04

### 新特性
- **前端 schema 展示注解**：列级新增 `@label` / `@desc` / `@order` / `@options` 四个注解，仅作用于 `_query-schema` 输出，前端渲染列表页可直接定制展示形态：

  ```sql
  COMMENT ON COLUMN users.status IS '状态 [@label: 用户状态 @desc: 0：禁用、1：启用 @order: 10 @dynamic: exact, in @sortable]';
  COMMENT ON COLUMN users.role   IS '角色 [@options: admin=管理员 user=普通用户 @dynamic: fuzzy]';
  ```

  - `@label` 覆盖前端展示的 `label`（注释自由文本保留为人类注释，不受影响）；
  - `@desc` 为字段增加 `desc` 描述（枚举值释义、单位说明等）；
  - `@order` 控制字段在 schema `fields` 数组中的顺序（仅影响 schema 输出，协议校验保持 DDL 声明序）；
  - `@options` 内联静态字典选项，前端可直接渲染下拉/多选控件，静态场景无需再写 options 接口；动态选项场景仍用 `@api`（二者同列互斥）。
- **where 条件操作符常量**：组装 typed 条件时 `Condition.Op` 可直接赋值 `dynq` 操作符常量（如 `dynq.OpContains`），不再需要手写 `string(...)` 转换；JSON 协议形态不变。

[Unreleased]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.6.0...HEAD
[v0.6.0]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.5.1...v0.6.0
[v0.5.1]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.5.0...v0.5.1
[v0.5.0]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.4.0...v0.5.0
[v0.4.0]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.3.0...v0.4.0
