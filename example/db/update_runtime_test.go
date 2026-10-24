package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"example/db/gen"

	"github.com/jninng/sqlc_gen_dynamic/dynq"
)

// fakeDB 记录 Exec 的 SQL / 参数并返回固定行数;Query / QueryRow 只为满足
// DBTX 接口(本文件的生成更新面只走 Exec)。
type fakeDB struct {
	query string
	args  []any
	calls int
	rows  int64
}

func (f *fakeDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	f.calls++
	f.query, f.args = query, args
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", f.rows)), nil
}

func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

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
