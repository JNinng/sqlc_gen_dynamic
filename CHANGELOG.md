# Changelog

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### 新特性
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

[Unreleased]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.5.1...HEAD
[v0.5.1]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.5.0...v0.5.1
[v0.5.0]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.4.0...v0.5.0
[v0.4.0]: https://github.com/JNinng/sqlc_gen_dynamic/compare/v0.3.0...v0.4.0
