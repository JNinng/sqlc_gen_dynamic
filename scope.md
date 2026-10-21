# 行级数据权限(@scope)

表级指令 `@scope: <列> <op> <函数>(<参数>,...) ...` 为动态表声明行级数据权限:权限规则集中在 DB 守卫函数里只写一次(显式参数、可单独 `SELECT * FROM fn(...)` 测试、定义在 sqlc 可见的 schema 内随迁移版本化),业务代码零权限感知。example 的 `customers`(三条 OR 守卫:归属用户集合 / 归属部门集合 / 显式共享字面量参数)、`projects`(eq 租户标量守卫)与 `sys_user`(in_any 数组三态守卫:NULL=全租户放行 / {}=拒绝 / 非空=ANY)即完整演示。

- DSL 语法(op 封闭集合、参数类型词表、SQL 形态) → 见 [dsl.md §5](./dsl.md#5-行级数据权限scope)
- 查询入参契约、游标与权限谓词的交互 → 见 [README](./README.md)
- 守卫函数实现示例 → 见 [example/db/schema.sql](./example/db/schema.sql)

## 机制要点

- 多守卫恒为 **OR 组合**(任一命中即可见);`buildXxxWhere` 头部按声明内联生成守卫谓词(零反射,编译期类型检查),与业务条件 AND;
- IN-子查询与不相关标量函数在单条语句内只求值一次(`in_any` 谓词中 fn 出现两次——IS NULL 判别与 ANY 匹配,STABLE 函数两次调用结果一致);List 与 Count 共用 → **total 与 items 的权限视图天然一致**;
- **fail-closed**:声明 `@scope` 的表,`params.Scope` 为 nil 时 List/Count 直接报错(`Kind=scope`),宁可拒绝服务也不静默退化为全量可见;
- **协议门结构性隔离**:客户端 JSON 不存在设置 Scope 的通道,权限值只能来自服务端注入。

## 调用形态

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
