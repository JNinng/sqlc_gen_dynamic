# Changelog

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

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
