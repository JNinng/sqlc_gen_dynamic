// main 是 example 工程的动态查询演示入口:启动即连接本地 PostgreSQL,
// 重建独立测试库 → 应用 schema.sql → 种子数据 → 跑一组动态 List 查询演示
// (过滤 / 冲突拒绝 / 分页双模式 / 游标 / 窄投影 / opts 禁用 / schema /
// 跨表游标互斥 / @scope 行级数据权限)。
//
// 运行:go run ./example/db
// 连接:默认 postgres://postgresql:root@localhost:5432/postgres(管理库),
// 可经 DYNQ_TEST_ADMIN_DSN 覆盖;测试库 sqlc_gen_dynamic_test 自动重建。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sqlc_gen_dynamic/example/db/gen"
	"sqlc_gen_dynamic/internal/dynq"
)

const (
	adminDSNDefault = "postgres://postgresql:root@localhost:5432/postgres"
	testDBName      = "sqlc_gen_dynamic_test"
)

// 种子数据(users 12 行:覆盖 NULL 组、NaN/±Inf、模糊转义、跨 org;
// 另含 @scope 演示数据:部门闭包 / 员工 / customer 归属)。
const seedSQL = `
TRUNCATE users, orders, customers, projects, sys_user, sys_dept_closure, sys_resource_grant RESTART IDENTITY CASCADE;
INSERT INTO users (name, email, password, status, role, age, score, balance, birth_date, org_id, tags, meta, created_at, updated_at, deleted_at) VALUES
('Alice',    'alice@x.com',  'pw', 'active',   'admin', 30, 90.5,  100.25, '1996-03-01', '0f14d0ab-9605-4a62-a9e4-5ed26688389b', ARRAY[1,2], '{"a":1,"b":{"c":2}}', '2026-01-01 10:00:00+00', '2026-02-01 08:00:00+00', NULL),
('Bob',      'bob@x.com',    'pw', 'banned',   NULL,    NULL, NULL, NULL,   NULL,         NULL,                                   NULL,      NULL,                 '2026-01-02 10:00:00+00', NULL,                     '2026-03-01 00:00:00+00'),
('Carol',    'carol@x.com',  'pw', 'active',   'user',  25, 50,    0,      '2001-01-15', '0f14d0ab-9605-4a62-a9e4-5ed26688389b', ARRAY[2,3], '{"a":2}',             '2026-01-03 10:00:00+00', '2026-01-05 08:00:00+00', NULL),
('Dave',     'dave@x.com',   'pw', 'inactive', 'user',  35, 75.5,  250.75, '1990-12-31', '1f14d0ab-9605-4a62-a9e4-5ed26688389b', ARRAY[5],   '{"b":{"c":3}}',       '2026-01-04 10:00:00+00', '2026-01-04 08:00:00+00', NULL),
('alicia',   'alicia@x.com', 'pw', 'active',   NULL,    28, 60,    10,     NULL,         NULL,                                   NULL,      NULL,                 '2026-01-05 10:00:00+00', '2026-01-06 08:00:00+00', NULL),
('100%_Sure','sure@x.com',   'pw', 'active',   NULL,    NULL, 70, NULL,    NULL,         NULL,                                   NULL,      NULL,                 '2026-01-06 10:00:00+00', NULL,                     NULL),
('NanRow',   'nan@x.com',    'pw', 'active',   NULL,    NULL, 'NaN', NULL, NULL,         NULL,                                   NULL,      NULL,                 '2026-01-07 10:00:00+00', NULL,                     NULL),
('InfRow',   'inf@x.com',    'pw', 'active',   NULL,    NULL, 'Infinity', NULL, NULL,    NULL,                                   NULL,      NULL,                 '2026-01-08 10:00:00+00', '2026-01-08 09:00:00+00', NULL),
('ZeroRow',  'zero@x.com',   'pw', 'active',   NULL,    NULL, 0,   NULL,    NULL,         NULL,                                   NULL,      NULL,                 '2026-01-09 10:00:00+00', NULL,                     NULL),
('NegInfRow','neginf@x.com', 'pw', 'active',   NULL,    NULL, '-Infinity', NULL, NULL,   NULL,                                   NULL,      NULL,                 '2026-01-10 10:00:00+00', '2026-01-10 09:00:00+00', NULL),
('%Wild%',   'wild@x.com',   'pw', 'active',   NULL,    40, NULL, NULL,    NULL,         NULL,                                   NULL,      NULL,                 '2026-01-11 10:00:00+00', NULL,                     NULL),
('Dup',      'dup@x.com',    'pw', 'active',   NULL,    30, 91,   NULL,    NULL,         NULL,                                   NULL,      NULL,                 '2026-01-12 10:00:00+00', NULL,                     NULL);
INSERT INTO orders (order_no, user_id, amount_cents, status, created_at) VALUES
('A-1', (SELECT id FROM users WHERE name='Alice'), 1000, 'paid',    '2026-02-01 10:00:00+00'),
('A-2', (SELECT id FROM users WHERE name='Alice'), 2500, 'pending', '2026-02-02 10:00:00+00'),
('C-1', (SELECT id FROM users WHERE name='Carol'),  500, 'shipped', '2026-02-03 10:00:00+00');

-- @scope 演示数据:组织架构 d_root → {d_eng, d_sales}(闭包含自身配对);
-- 租户 t_acme / t_globe;customers 六行 + projects 三行 + 一条显式共享授权。
INSERT INTO sys_dept_closure (ancestor_id, descendant_id) VALUES
('d_root', 'd_root'), ('d_root', 'd_eng'), ('d_root', 'd_sales'),
('d_eng',  'd_eng'),  ('d_sales', 'd_sales');
INSERT INTO sys_user (id, name, owner_dept_id, tenant_id) VALUES
('u1', 'Alice(工程部)', 'd_eng',  't_acme'),
('u2', 'Bob(工程部)',   'd_eng',  't_acme'),
('u3', 'Carol(销售部)', 'd_sales', 't_globe'),
('u4', 'Diana(集团)',   'd_root', 't_acme');
INSERT INTO customers (name, amount_cents, owner_user_id, owner_dept_id, created_at) VALUES
('Acme建站',     12000, 'u1', 'd_eng',   '2026-03-01 10:00:00+00'),
('Bob物流系统',  30000, 'u2', 'd_eng',   '2026-03-02 10:00:00+00'),
('Bob运维合同',   8000, 'u2', 'd_eng',   '2026-03-03 10:00:00+00'),
('Carol营销季报', 5000, 'u3', 'd_sales', '2026-03-04 10:00:00+00'),
('外部门协作单',  2000, 'u9', 'd_eng',   '2026-03-05 10:00:00+00'),
('集团战略客户', 99000, 'u4', 'd_root',  '2026-03-06 10:00:00+00');
-- Acme建站 显式共享给 Diana:守卫 fn_permitted('customers', user_id) 消费。
INSERT INTO sys_resource_grant (resource, user_id, record_id) VALUES
('customers', 'u4', (SELECT id FROM customers WHERE name = 'Acme建站'));
INSERT INTO projects (name, tenant_id, owner_user_id, created_at) VALUES
('Portal重构',    't_acme',  'u1', '2026-04-01 10:00:00+00'),
('移动端v2',      't_acme',  'u2', '2026-04-02 10:00:00+00'),
('Globe年度审计', 't_globe', 'u3', '2026-04-03 10:00:00+00');
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	adminDSN := os.Getenv("DYNQ_TEST_ADMIN_DSN")
	if adminDSN == "" {
		adminDSN = adminDSNDefault
	}
	pool, err := setup(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer pool.Close()

	fmt.Println("== 环境 ==")
	fmt.Printf("   测试库: %s\n", testDBName)
	fmt.Printf("   schema: example/db/schema.sql(注释 DSL 生成 registry)\n\n")

	fmt.Println("== ① 基础过滤 + total(status=active, name 模糊 contains 'row') ==")
	demoFilter(ctx, pool)
	fmt.Println("== ② 条件冲突显式拒绝(between 与独立 gte 重叠) ==")
	demoConflict(pool)
	fmt.Println("== ③ offset 分页(page 2, page_size 3,默认排序 created_at DESC) ==")
	demoOffset(ctx, pool)
	fmt.Println("== ④ 游标模式翻全页(score ASC, 12 行 × page_size 5) ==")
	demoCursor(ctx, pool)
	fmt.Println("== ⑤ 窄投影(fields=[name],id/createdAt 强制并入) ==")
	demoProjection(ctx, pool)
	fmt.Println("== ⑥ opts 禁用(role 域禁用后携带非零值 → 显式拒绝) ==")
	demoDisable(ctx, pool)
	fmt.Println("== ⑦ 前端 schema(UsersQuerySchema JSON,无禁用投影) ==")
	demoSchema()
	fmt.Println("== ⑧ 跨表游标互斥(users 游标用于 orders → 指纹不符) ==")
	demoCrossTable(ctx, pool)
	fmt.Println("== ⑨ @scope 行级数据权限(多守卫 OR/eq/字面量 + fail-closed) ==")
	demoScope(ctx, pool)

	fmt.Println("\n全部演示完成 ✔")
	return nil
}

// setup 重建独立测试库并应用 schema + 种子。
func setup(ctx context.Context, adminDSN string) (*pgxpool.Pool, error) {
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return nil, fmt.Errorf("连接管理库失败(本地 PostgreSQL 不可达?): %w", err)
	}
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+testDBName); err != nil {
		admin.Close(ctx)
		return nil, err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+testDBName); err != nil {
		admin.Close(ctx)
		return nil, err
	}
	admin.Close(ctx)

	pool, err := pgxpool.New(ctx, dsnForDB(adminDSN, testDBName))
	if err != nil {
		return nil, err
	}
	schemaSQL, err := os.ReadFile(schemaPath())
	if err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema.sql: %w", err)
	}
	if _, err := pool.Exec(ctx, seedSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("seed: %w", err)
	}
	return pool, nil
}

// dsnForDB 将管理 DSN 的库名替换为 testDB。
func dsnForDB(dsn, db string) string {
	lastSlash := -1
	for i := len(dsn) - 1; i >= 0; i-- {
		if dsn[i] == '/' {
			lastSlash = i
			break
		}
	}
	if lastSlash < 0 {
		return dsn
	}
	return dsn[:lastSlash+1] + db
}

// schemaPath 返回 main.go 同目录的 schema.sql(与运行目录无关)。
func schemaPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "schema.sql"
	}
	return filepath.Join(filepath.Dir(file), "schema.sql")
}

// ---- 演示函数 ----

func mustParse(q *dynq.Query, opts ...dynq.QueryOption) gen.ListUsersParams {
	p, err := gen.ParseUsersQuery(q, opts...)
	if err != nil {
		panic(fmt.Sprintf("ParseUsersQuery: %v", err))
	}
	return p
}

func demoFilter(ctx context.Context, pool *pgxpool.Pool) {
	q := &dynq.Query{
		Where: []dynq.Condition{
			{Field: "status", Op: "eq", Value: "active"},
			{Field: "name", Op: "contains", Value: "row"},
		},
	}
	p := mustParse(q)
	total, err := gen.CountUsers(ctx, pool, p)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	res, err := gen.ListUsers(ctx, pool, p)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	fmt.Printf("   total=%d\n", total)
	for _, u := range res.Items {
		fmt.Printf("   [%d] %s | %s | score=%v\n", u.ID, u.Name, u.Status, scoreOf(u.Score))
	}
	fmt.Println()
}

func demoConflict(pool *pgxpool.Pool) {
	_, err := gen.ParseUsersQuery(&dynq.Query{
		Where: []dynq.Condition{
			{Field: "age", Op: "between", Value: 20, RightValue: 30},
			{Field: "age", Op: "gte", Value: 25},
		},
	})
	if err == nil {
		fmt.Println("   !! 未拒绝冲突(不应发生)")
		return
	}
	e := dynq.AsError(err)
	fmt.Printf("   %s (Kind=%s, Index=%d)\n", e.Reason, e.Kind, e.Index)
	fmt.Println()
}

func demoOffset(ctx context.Context, pool *pgxpool.Pool) {
	const page, pageSize = 2, 3
	p := mustParse(&dynq.Query{Pagination: &dynq.Pagination{Page: intPtr(page), PageSize: intPtr(pageSize)}})
	res, err := gen.ListUsers(ctx, pool, p)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	total, err := gen.CountUsers(ctx, pool, p)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	// 返回信封:total / items 行数 / page / page_size / has_more / next_cursor(items 明细略)。
	fmt.Printf("   total=%d items=%d page=%d page_size=%d has_more=%v next_cursor=%q\n",
		total, len(res.Items), page, pageSize, res.HasMore, truncate(res.NextCursor, 24))
	fmt.Println()
}

func demoCursor(ctx context.Context, pool *pgxpool.Pool) {
	// total 与翻页位置无关:循环外统计一次,各页应保持一致。
	base := mustParse(&dynq.Query{Sort: []dynq.SortItem{{Field: "score", Order: "asc"}}})
	total, err := gen.CountUsers(ctx, pool, base)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	after := ""
	var ids []int64
	for page := 1; ; page++ {
		pq := &dynq.Query{
			Sort:       []dynq.SortItem{{Field: "score", Order: "asc"}},
			Pagination: &dynq.Pagination{PageSize: intPtr(5)},
		}
		if after != "" {
			pq.Pagination.After = &after
		}
		p := mustParse(pq)
		res, err := gen.ListUsers(ctx, pool, p)
		if err != nil {
			fmt.Println("   error:", err)
			return
		}
		for _, u := range res.Items {
			ids = append(ids, u.ID)
		}
		// 返回信封:total / items 行数 / has_more / next_cursor(items 明细略);
		// 首页为 offset 模式,续翻后各页携带上一页的 next_cursor。
		fmt.Printf("   第 %d 页: total=%d items=%d has_more=%v next_cursor=%q\n",
			page, total, len(res.Items), res.HasMore, truncate(res.NextCursor, 24))
		if !res.HasMore || res.NextCursor == "" {
			break
		}
		after = res.NextCursor
		if page > 10 {
			fmt.Println("   !! 游标翻页未终止")
			return
		}
	}
	fmt.Printf("   共 %d 行(唯一性 %v)\n", len(ids), unique(ids))
	fmt.Println()
}

func demoProjection(ctx context.Context, pool *pgxpool.Pool) {
	p := mustParse(&dynq.Query{
		Fields:     []string{"name"},
		Pagination: &dynq.Pagination{PageSize: intPtr(3)},
	})
	res, err := gen.ListUsers(ctx, pool, p)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	for _, u := range res.Items {
		fmt.Printf("   [%d] name=%q email=%q(零值=未查) created_at=%v(强制并入)\n",
			u.ID, u.Name, u.Email, u.CreatedAt.Valid)
	}
	fmt.Println()
}

func demoDisable(ctx context.Context, pool *pgxpool.Pool) {
	opts := []dynq.QueryOption{dynq.DisableWhere(gen.UserField.Role)}
	// typed 门:禁用字段携带非零值 → error(显式拒绝,不静默剔除)。
	p := mustParse(&dynq.Query{})
	role := "admin"
	p.RoleContains = role
	_, err := gen.ListUsers(ctx, pool, p, opts...)
	if err == nil {
		fmt.Println("   !! 禁用字段未被拒绝(不应发生)")
		return
	}
	e := dynq.AsError(err)
	fmt.Printf("   %s (Kind=%s, Field=%s)\n", e.Reason, e.Kind, e.Field)
	// 同 opts 下 schema 反映禁用:role 的 op 集清空。
	s := gen.UsersQuerySchema(opts...)
	for _, f := range s.Fields {
		if f.Field == "role" {
			fmt.Printf("   schema 中 role 的 ops=%v(禁用后清空)\n", f.Ops)
		}
	}
	fmt.Println()
}

func demoSchema() {
	s := gen.UsersQuerySchema()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	// 只展示前几个字段 + 分页信息,避免刷屏。
	var pretty map[string]any
	_ = json.Unmarshal(b, &pretty)
	fields := pretty["fields"].([]any)
	pretty["fields"] = fields[:min(4, len(fields))]
	pretty["..."] = fmt.Sprintf("共 %d 个字段(完整见 UsersQuerySchema())", len(fields))
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Printf("%s\n", out)
	fmt.Println()

	// @api 选项数据源契约(orders.user_id):前端据此渲染远程选择器——
	// 值控件 options 经 GET /api/v1/users/options 拉取,每项取 id / name。
	os := gen.OrdersQuerySchema()
	for _, f := range os.Fields {
		if f.API == nil {
			continue
		}
		apiJSON, _ := json.Marshal(f.API)
		fmt.Printf("   orders.%s @api 契约: %s\n", f.Field, apiJSON)
	}
	fmt.Println()
}

func demoCrossTable(ctx context.Context, pool *pgxpool.Pool) {
	// 先取 users 的游标。
	p := mustParse(&dynq.Query{Pagination: &dynq.Pagination{PageSize: intPtr(3)}})
	res, err := gen.ListUsers(ctx, pool, p)
	if err != nil || res.NextCursor == "" {
		fmt.Println("   error(取游标失败):", err)
		return
	}
	usersCursor := res.NextCursor
	// 同一游标用于 orders(同规格默认排序 created_at DESC + id)→ 指纹不符。
	op, err := gen.ParseOrdersQuery(&dynq.Query{Pagination: &dynq.Pagination{After: &usersCursor}})
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	_, err = gen.ListOrders(ctx, pool, op)
	if err == nil {
		fmt.Println("   !! 跨表游标未被拒绝(不应发生)")
		return
	}
	e := dynq.AsError(err)
	fmt.Printf("   %s (Kind=%s)\n", e.Reason, e.Kind)
	fmt.Println()
}

// demoScope 演示 @scope 行级数据权限:守卫函数只写一次,多守卫 OR 组合
// (归属用户 / 归属部门 / 显式共享字面量参数),scopes int[] 承载多角色并集;
// projects 演示 eq 标量守卫(租户隔离);缺省 Scope 时 fail-closed 报错。
func demoScope(ctx context.Context, pool *pgxpool.Pool) {
	type view struct {
		label  string
		userID string
		scopes []int64
	}
	views := []view{
		{"Alice(u1) scopes=[1] 仅本人", "u1", []int64{1}},
		{"Diana(u4) scopes=[2] 本部门", "u4", []int64{2}},
		{"Diana(u4) scopes=[1,7] 多角色并集(部门主管+全局审计)", "u4", []int64{1, 7}},
		{"Diana(u4) scopes=[3] 本部门及子部门", "u4", []int64{3}},
	}
	for _, v := range views {
		// middleware 语义:认证解析后 WithScope 一次,handler 一行取出注入;
		// Scope 类型按资源生成(CustomersScope),字段与守卫声明参数一一对应。
		authed := dynq.WithScope(ctx, &gen.CustomersScope{UserID: v.userID, Scopes: v.scopes})
		p, err := gen.ParseCustomersQuery(&dynq.Query{})
		if err != nil {
			fmt.Println("   error:", err)
			return
		}
		p.Scope = dynq.ScopeFromContext[gen.CustomersScope](authed)
		total, err := gen.CountCustomers(authed, pool, p)
		if err != nil {
			fmt.Println("   error:", err)
			return
		}
		res, err := gen.ListCustomers(authed, pool, p)
		if err != nil {
			fmt.Println("   error:", err)
			return
		}
		names := make([]string, 0, len(res.Items))
		for _, c := range res.Items {
			names = append(names, c.Name)
		}
		fmt.Printf("   %s → total=%d items=%v\n", v.label, total, names)
	}

	// 业务条件与权限正交叠加:scope=3 全集内再按 name 过滤。
	authed := dynq.WithScope(ctx, &gen.CustomersScope{UserID: "u4", Scopes: []int64{3}})
	p, err := gen.ParseCustomersQuery(&dynq.Query{
		Where: []dynq.Condition{{Field: "name", Op: "contains", Value: "Bob"}},
	})
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	p.Scope = dynq.ScopeFromContext[gen.CustomersScope](authed)
	res, err := gen.ListCustomers(authed, pool, p)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	fmt.Printf("   scopes=[3] + name contains Bob → %d 行(权限视图内收窄)\n", len(res.Items))

	// eq 标量守卫:projects 只看本租户(Diana ∈ t_acme,不见 t_globe 项目)。
	authedProj := dynq.WithScope(ctx, &gen.ProjectsScope{UserID: "u4"})
	pp, err := gen.ParseProjectsQuery(&dynq.Query{})
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	pp.Scope = dynq.ScopeFromContext[gen.ProjectsScope](authedProj)
	projTotal, err := gen.CountProjects(authedProj, pool, pp)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	projRes, err := gen.ListProjects(authedProj, pool, pp)
	if err != nil {
		fmt.Println("   error:", err)
		return
	}
	projNames := make([]string, 0, len(projRes.Items))
	for _, pr := range projRes.Items {
		projNames = append(projNames, pr.Name)
	}
	fmt.Printf("   Diana projects(eq 租户守卫) → total=%d items=%v\n", projTotal, projNames)

	// fail-closed:声明 @scope 的表未注入 Scope → 显式报错,不静默全量。
	_, err = gen.ListCustomers(ctx, pool, gen.ListCustomersParams{})
	if err == nil {
		fmt.Println("   !! 缺省 Scope 未被拒绝(不应发生)")
		return
	}
	e := dynq.AsError(err)
	fmt.Printf("   缺省 Scope → %s (Kind=%s)\n", e.Reason, e.Kind)
	fmt.Println()
}

// ---- 小工具 ----

func scoreOf(s *float64) string {
	if s == nil {
		return "NULL"
	}
	return fmt.Sprintf("%v", *s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func unique(ids []int64) bool {
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func intPtr(n int) *int { return &n }
