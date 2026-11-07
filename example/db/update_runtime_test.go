package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"example/db/gen"

	"github.com/jninng/sqlc_gen_dynamic/dynq"
)

// fakeDB 记录 Exec 的 SQL / 参数并返回固定行数;Query 回放 canned 行
// (RETURNING 入口走 Query / 收行,Exec 计数仅用于守卫断言)。
type fakeDB struct {
	query   string
	args    []any
	calls   int
	queries int
	rows    int64
	qrows   *fakeRows
}

func (f *fakeDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	f.calls++
	f.query, f.args = query, args
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", f.rows)), nil
}

func (f *fakeDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	f.queries++
	f.query, f.args = query, args
	if f.qrows == nil {
		return nil, errors.New("unexpected Query")
	}
	return f.qrows, nil
}

func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

// fakeRows 是满足 pgx.Rows 的最小桩:按 FieldDescriptions 名序回放 canned 行。
// canned 值须与目标模型字段同型(Scan 按反射直接赋值,不做类型收敛),NULL 用
// 无类型 nil 表示;列可只返回模型列的子集(RowToStructByNameLax 按名 lax 匹配)。
type fakeRows struct {
	descs []pgconn.FieldDescription
	vals  [][]any
	i     int
}

func newFakeRows(names []string, vals [][]any) *fakeRows {
	descs := make([]pgconn.FieldDescription, len(names))
	for i, n := range names {
		descs[i] = pgconn.FieldDescription{Name: n}
	}
	return &fakeRows{descs: descs, vals: vals}
}

func (f *fakeRows) Next() bool {
	if f.i < len(f.vals) {
		f.i++
		return true
	}
	return false
}

func (f *fakeRows) Scan(dest ...any) error {
	row := f.vals[f.i-1]
	if len(row) != len(dest) {
		return fmt.Errorf("fakeRows: row %d has %d values, got %d dests", f.i, len(row), len(dest))
	}
	for j, d := range dest {
		target := reflect.ValueOf(d).Elem()
		src := reflect.ValueOf(row[j])
		if !src.IsValid() {
			target.Set(reflect.Zero(target.Type())) // NULL
			continue
		}
		if src.Type() != target.Type() {
			return fmt.Errorf("fakeRows: column %s value %T cannot scan into %s", f.descs[j].Name, row[j], target.Type())
		}
		target.Set(src)
	}
	return nil
}

func (f *fakeRows) Close()                                       {}
func (f *fakeRows) Err() error                                   { return nil }
func (f *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (f *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return f.descs }
func (f *fakeRows) Values() ([]any, error)                       { return nil, errors.New("unexpected Values") }
func (f *fakeRows) RawValues() [][]byte                          { return nil }
func (f *fakeRows) Conn() *pgx.Conn                              { return nil }

func hasArg(args []any, want any) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// hasStringArg 兼容值类型与可空列的 *string(生成 Set 传指针,编码前 fake DB
// 看到的是指针本身)。
func hasStringArg(args []any, want string) bool {
	for _, a := range args {
		switch v := a.(type) {
		case string:
			if v == want {
				return true
			}
		case *string:
			if v != nil && *v == want {
				return true
			}
		}
	}
	return false
}

// hasNilStringArg 报告参数里是否有显式 NULL(可空列 Set(nil) 传 *string(nil))。
func hasNilStringArg(args []any) bool {
	for _, a := range args {
		if p, ok := a.(*string); ok && p == nil {
			return true
		}
	}
	return false
}

// assertNotExecuted 断言校验失败发生在 Exec 之前(不发全表 UPDATE)。
func assertNotExecuted(t *testing.T, db *fakeDB) {
	t.Helper()
	if db.calls != 0 {
		t.Fatalf("expected no Exec, got %d call(s): %s", db.calls, db.query)
	}
}

// TestUpdateUsersRuntime 用假 DBTX 执行生成更新面,校验 SQL 形态、参数与
// 校验守卫(不依赖数据库)。
func TestUpdateUsersRuntime(t *testing.T) {
	ctx := context.Background()

	t.Run("pk_locator_with_filter", func(t *testing.T) {
		db := &fakeDB{rows: 1}
		n, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetName("Alice"),
			gen.UserUpdate.WhereIDEq(7),
		)
		if err != nil {
			t.Fatalf("UpdateUsers: %v", err)
		}
		if n != 1 {
			t.Fatalf("rows: %d, want 1", n)
		}
		for _, want := range []string{"UPDATE users SET", "name = $", "deleted_at IS NULL", "id = $"} {
			if !strings.Contains(db.query, want) {
				t.Errorf("query missing %q: %s", want, db.query)
			}
		}
		if !hasArg(db.args, "Alice") || !hasArg(db.args, int64(7)) {
			t.Errorf("args: %v", db.args)
		}
	})

	t.Run("without_filter", func(t *testing.T) {
		db := &fakeDB{rows: 1}
		if _, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetRole(dynq.Ptr("ops")),
			gen.UserUpdate.WhereIDEq(2),
			gen.UserUpdate.WithoutFilter(),
		); err != nil {
			t.Fatalf("UpdateUsers: %v", err)
		}
		if strings.Contains(db.query, "deleted_at") {
			t.Errorf("@filter must be bypassed: %s", db.query)
		}
		if !strings.Contains(db.query, "id = $") || !hasStringArg(db.args, "ops") {
			t.Errorf("caller condition lost: %s %v", db.query, db.args)
		}
	})

	t.Run("explicit_null", func(t *testing.T) {
		db := &fakeDB{rows: 1}
		if _, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetRole(nil),
			gen.UserUpdate.WhereIDEq(1),
		); err != nil {
			t.Fatalf("UpdateUsers: %v", err)
		}
		if !hasNilStringArg(db.args) {
			t.Errorf("explicit NULL must be passed as nil *string arg: %v", db.args)
		}
		if !strings.Contains(db.query, "role = $") {
			t.Errorf("explicit NULL must generate SET clause: %s", db.query)
		}
	})

	t.Run("enum_in_placeholders", func(t *testing.T) {
		db := &fakeDB{rows: 2}
		n, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetName("x"),
			gen.UserUpdate.WhereStatusIn(gen.UserStatusActive, gen.UserStatusBanned),
		)
		if err != nil {
			t.Fatalf("UpdateUsers: %v", err)
		}
		if n != 2 {
			t.Fatalf("rows: %d, want 2", n)
		}
		if !strings.Contains(db.query, "status IN ($") {
			t.Errorf("IN clause missing: %s", db.query)
		}
		if !hasArg(db.args, gen.UserStatusActive) || !hasArg(db.args, gen.UserStatusBanned) {
			t.Errorf("enum args: %v", db.args)
		}
	})

	t.Run("zero_set", func(t *testing.T) {
		db := &fakeDB{}
		_, err := gen.UpdateUsers(ctx, db, gen.UserUpdate.WhereIDEq(1))
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindSet || !strings.Contains(e.Reason, "empty set") {
			t.Fatalf("want KindSet empty set, got %v", err)
		}
		assertNotExecuted(t, db)
	})

	t.Run("zero_caller_where_not_exempted_by_filter", func(t *testing.T) {
		db := &fakeDB{}
		_, err := gen.UpdateUsers(ctx, db, gen.UserUpdate.SetName("x"))
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindWhere || !strings.Contains(e.Reason, "zero where") {
			t.Fatalf("want KindWhere zero where, got %v", err)
		}
		assertNotExecuted(t, db)
	})

	t.Run("between_inverted", func(t *testing.T) {
		db := &fakeDB{}
		_, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetAge(dynq.Ptr(int64(1))),
			gen.UserUpdate.WhereAgeBetween(10, 5),
		)
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindWhere || !strings.Contains(e.Reason, "lower bound exceeds upper bound") {
			t.Fatalf("want between order error, got %v", err)
		}
		assertNotExecuted(t, db)
	})

	t.Run("empty_in", func(t *testing.T) {
		db := &fakeDB{}
		_, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetName("x"),
			gen.UserUpdate.WhereIDIn(),
		)
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindWhere || !strings.Contains(e.Reason, "must not be empty") {
			t.Fatalf("want empty in error, got %v", err)
		}
		assertNotExecuted(t, db)
	})

	t.Run("slot_conflict", func(t *testing.T) {
		db := &fakeDB{}
		_, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetAge(dynq.Ptr(int64(1))),
			gen.UserUpdate.WhereIDEq(1),
			gen.UserUpdate.WhereIDEq(2),
		)
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindWhere || !strings.Contains(e.Reason, "conflicting conditions") {
			t.Fatalf("want slot conflict, got %v", err)
		}
		assertNotExecuted(t, db)
	})

	t.Run("empty_fuzzy_pattern", func(t *testing.T) {
		db := &fakeDB{}
		_, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetName("x"),
			gen.UserUpdate.WhereNameContains(""),
		)
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindWhere || !strings.Contains(e.Reason, "empty pattern") {
			t.Fatalf("want empty pattern error, got %v", err)
		}
		assertNotExecuted(t, db)
	})

	t.Run("duplicate_set", func(t *testing.T) {
		db := &fakeDB{}
		_, err := gen.UpdateUsers(ctx, db,
			gen.UserUpdate.SetName("a"),
			gen.UserUpdate.SetName("b"),
			gen.UserUpdate.WhereIDEq(1),
		)
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindSet || !strings.Contains(e.Reason, "conflicting set") {
			t.Fatalf("want set conflict, got %v", err)
		}
		assertNotExecuted(t, db)
	})
}

// TestUpdateNoPkRuntime 覆盖无 @pk 表的复合列定位 SQL(零 where 守卫只保证
// 至少一个条件;本例由调用方给出完整业务键)。
func TestUpdateNoPkRuntime(t *testing.T) {
	db := &fakeDB{rows: 1}
	n, err := gen.UpdateSysResourceGrant(context.Background(), db,
		gen.SysResourceGrantUpdate.SetNote(dynq.Ptr("显式共享")),
		gen.SysResourceGrantUpdate.WhereResourceEq("customers"),
		gen.SysResourceGrantUpdate.WhereUserIDEq("u4"),
	)
	if err != nil {
		t.Fatalf("UpdateSysResourceGrant: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows: %d, want 1", n)
	}
	for _, want := range []string{"UPDATE sys_resource_grant SET", "note = $", "resource = $", "user_id = $"} {
		if !strings.Contains(db.query, want) {
			t.Errorf("query missing %q: %s", want, db.query)
		}
	}
	if !hasStringArg(db.args, "customers") || !hasStringArg(db.args, "u4") || !hasStringArg(db.args, "显式共享") {
		t.Errorf("args: %v", db.args)
	}
}

// TestUpdateScopeDecoupled 覆盖 @scope 表:Update 不取 Scope、不平移权限谓词,
// 无需 ctx 注入即可执行(权限由读取阶段保证)。
func TestUpdateScopeDecoupled(t *testing.T) {
	db := &fakeDB{rows: 1}
	if _, err := gen.UpdateCustomers(context.Background(), db,
		gen.CustomerUpdate.SetAmountCents(15000),
		gen.CustomerUpdate.WhereIDEq(1),
	); err != nil {
		t.Fatalf("UpdateCustomers without scope: %v", err)
	}
	if strings.Contains(db.query, "fn_accessible") || strings.Contains(db.query, "fn_permitted") {
		t.Errorf("@scope must not be inlined into UPDATE: %s", db.query)
	}
}

// TestUpdateReturningRuntime 用 canned 行执行 RETURNING 入口:SQL 形态、
// 强类型收行、零命中语义、守卫一致性与 @filter 行为(不依赖数据库)。
func TestUpdateReturningRuntime(t *testing.T) {
	ctx := context.Background()

	t.Run("multi_row_collect", func(t *testing.T) {
		db := &fakeDB{qrows: newFakeRows([]string{"id", "name"}, [][]any{
			{int64(3), "batch-3"},
			{int64(4), "batch-4"},
		})}
		users, err := gen.UpdateUsersReturning(ctx, db,
			gen.UserUpdate.SetRole(dynq.Ptr("batch")),
			gen.UserUpdate.WhereIDIn(3, 4),
		)
		if err != nil {
			t.Fatalf("UpdateUsersReturning: %v", err)
		}
		if len(users) != 2 || users[0].ID != 3 || users[1].ID != 4 || users[0].Name != "batch-3" {
			t.Fatalf("rows: %+v", users)
		}
		for _, want := range []string{"UPDATE users SET", "deleted_at IS NULL", "id IN ($", "RETURNING id, name, email"} {
			if !strings.Contains(db.query, want) {
				t.Errorf("query missing %q: %s", want, db.query)
			}
		}
		if !hasStringArg(db.args, "batch") {
			t.Errorf("args: %v", db.args)
		}
		if db.calls != 0 {
			t.Errorf("RETURNING entry must query, not exec: %d exec call(s)", db.calls)
		}
	})

	t.Run("by_pk_builtin_locator", func(t *testing.T) {
		db := &fakeDB{qrows: newFakeRows([]string{"id", "name"}, [][]any{{int64(7), "Alice"}})}
		u, err := gen.UpdateUsersReturningByPk(ctx, db, 7,
			gen.UserUpdate.SetName("Alice·改名"),
		)
		if err != nil {
			t.Fatalf("UpdateUsersReturningByPk: %v", err)
		}
		if u.ID != 7 || u.Name != "Alice" {
			t.Fatalf("row: %+v", u)
		}
		// 主键定位内建:无 opts Where 也合法(SQL 带 id = $),零 where 守卫
		// 由定位参数满足,不要求调用方再补一条 Where。
		for _, want := range []string{"UPDATE users SET", "deleted_at IS NULL", "id = $", "RETURNING"} {
			if !strings.Contains(db.query, want) {
				t.Errorf("query missing %q: %s", want, db.query)
			}
		}
		if !hasArg(db.args, int64(7)) || !hasStringArg(db.args, "Alice·改名") {
			t.Errorf("args: %v", db.args)
		}
	})

	t.Run("zero_hit", func(t *testing.T) {
		// 单行入口:零命中 = pgx.ErrNoRows。
		db := &fakeDB{qrows: newFakeRows([]string{"id"}, nil)}
		_, err := gen.UpdateUsersReturningByPk(ctx, db, 42, gen.UserUpdate.SetName("x"))
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("want pgx.ErrNoRows, got %v", err)
		}
		// 多行入口:零命中 = 空切片 + nil error。
		db = &fakeDB{qrows: newFakeRows([]string{"id"}, nil)}
		items, err := gen.UpdateUsersReturning(ctx, db,
			gen.UserUpdate.SetName("x"),
			gen.UserUpdate.WhereIDEq(42),
		)
		if err != nil || items == nil || len(items) != 0 {
			t.Fatalf("want empty slice + nil error, got %v / %v", items, err)
		}
	})

	t.Run("guards_before_query", func(t *testing.T) {
		// 零 SET:多行 / 单行入口同样拒绝。
		db := &fakeDB{}
		if _, err := gen.UpdateUsersReturning(ctx, db, gen.UserUpdate.WhereIDEq(1)); !strings.Contains(dynq.AsError(err).Reason, "empty set") {
			t.Fatalf("want empty set, got %v", err)
		}
		if db.queries != 0 {
			t.Errorf("guard must reject before Query, got %d", db.queries)
		}
		db = &fakeDB{}
		if _, err := gen.UpdateUsersReturningByPk(ctx, db, 1); !strings.Contains(dynq.AsError(err).Reason, "empty set") {
			t.Fatalf("want empty set (pk locator does not exempt), got %v", err)
		}
		// 零调用方 where:多行入口不豁免(@filter 不豁免零 where 守卫)。
		db = &fakeDB{}
		_, err := gen.UpdateUsersReturning(ctx, db, gen.UserUpdate.SetName("x"))
		if e := dynq.AsError(err); e == nil || e.Kind != dynq.KindWhere || !strings.Contains(e.Reason, "zero where") {
			t.Fatalf("want KindWhere zero where, got %v", err)
		}
		if db.queries != 0 {
			t.Errorf("guard must reject before Query, got %d", db.queries)
		}
	})

	t.Run("filter_bypass", func(t *testing.T) {
		db := &fakeDB{qrows: newFakeRows([]string{"id"}, [][]any{{int64(2)}})}
		if _, err := gen.UpdateUsersReturningByPk(ctx, db, 2,
			gen.UserUpdate.SetRole(dynq.Ptr("restored")),
			gen.UserUpdate.WithoutFilter(),
		); err != nil {
			t.Fatalf("UpdateUsersReturningByPk: %v", err)
		}
		// 绕过检查只看 WHERE 子句:RETURNING 列清单本就含 deleted_at 列名。
		where := db.query[:strings.Index(db.query, " RETURNING ")]
		if strings.Contains(where, "deleted_at") {
			t.Errorf("@filter must be bypassed: %s", db.query)
		}
	})

	t.Run("no_pk_multi_row", func(t *testing.T) {
		db := &fakeDB{qrows: newFakeRows([]string{"resource", "user_id", "record_id", "note"}, [][]any{
			{"customers", "u4", int64(9), nil},
		})}
		grants, err := gen.UpdateSysResourceGrantReturning(ctx, db,
			gen.SysResourceGrantUpdate.SetNote(dynq.Ptr("显式共享")),
			gen.SysResourceGrantUpdate.WhereResourceEq("customers"),
			gen.SysResourceGrantUpdate.WhereUserIDEq("u4"),
		)
		if err != nil {
			t.Fatalf("UpdateSysResourceGrantReturning: %v", err)
		}
		if len(grants) != 1 || grants[0].RecordID != 9 || grants[0].Note != nil {
			t.Fatalf("rows: %+v", grants)
		}
		if !strings.Contains(db.query, "UPDATE sys_resource_grant SET") ||
			!strings.Contains(db.query, "RETURNING resource, user_id, record_id, note") {
			t.Errorf("query: %s", db.query)
		}
	})
}
