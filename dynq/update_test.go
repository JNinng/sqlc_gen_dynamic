package dynq

import (
	"strings"
	"testing"

	sq "github.com/Masterminds/squirrel"
)

// 动态更新面运行时基型:选项收集 / 槽位冲突 / 校验边界 / 零 where 守卫。
// 表 marker 在测试内自建,生成代码的 marker 类型只负责编译期区分。

type updateTestTable struct{}

func newTestUpdateState() *UpdateState[updateTestTable] {
	return NewUpdateState[updateTestTable]()
}

// ---- 选项收集 ----

func TestUpdateStateCollect(t *testing.T) {
	st := newTestUpdateState()
	if err := st.Set("name", "name", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("orgId", "org_id", "::uuid", "0f14d0ab-9605-4a62-a9e4-5ed26688389b"); err != nil {
		t.Fatal(err)
	}
	if err := st.Where("id", OpEq, sq.Expr("id = ?", 7)); err != nil {
		t.Fatal(err)
	}
	if err := st.Where("name", OpContains, sq.Expr("name ILIKE ?", "%a%")); err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	sets := st.Sets()
	if len(sets) != 2 || sets[0].Column != "name" || sets[0].Cast != "" || sets[1].Column != "org_id" || sets[1].Cast != "::uuid" {
		t.Fatalf("sets: %+v", sets)
	}
	if got := len(st.Wheres()); got != 2 {
		t.Fatalf("wheres: %d", got)
	}
	// 选项顺序即 SQL 顺序:SET 条款按调用序,WHERE 条件按调用序。
	if sets[0].Value != "Alice" {
		t.Fatalf("set value: %v", sets[0].Value)
	}
	// Wheres 必须真的保存可执行条件(保存 nil 会让 ToSql panic 或丢条件)。
	for i, want := range []struct {
		sql  string
		args []any
	}{
		{"id = ?", []any{7}},
		{"name ILIKE ?", []any{"%a%"}},
	} {
		gotSQL, gotArgs, werr := st.Wheres()[i].ToSql()
		if werr != nil {
			t.Fatalf("where[%d] ToSql: %v", i, werr)
		}
		if gotSQL != want.sql {
			t.Errorf("where[%d] sql: %q, want %q", i, gotSQL, want.sql)
		}
		if len(gotArgs) != len(want.args) || gotArgs[0] != want.args[0] {
			t.Errorf("where[%d] args: %v, want %v", i, gotArgs, want.args)
		}
	}
}

// ---- 槽位冲突 ----

func TestUpdateSetConflict(t *testing.T) {
	st := newTestUpdateState()
	if err := st.Set("name", "name", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	err := st.Set("name", "name", "", "Bob")
	if err == nil {
		t.Fatal("duplicate set must be rejected")
	}
	e := AsError(err)
	if e.Kind != KindSet || e.Field != "name" || !strings.Contains(e.Reason, "conflicting set") {
		t.Fatalf("set conflict error: %+v", e)
	}
	if len(st.Sets()) != 1 {
		t.Fatal("rejected set must not be appended")
	}
}

func TestUpdateWhereConflict(t *testing.T) {
	st := newTestUpdateState()
	if err := st.Where("age", OpGte, sq.Expr("age >= ?", 20)); err != nil {
		t.Fatal(err)
	}
	// 同 (字段, op) 重复 → 拒绝。
	err := st.Where("age", OpGte, sq.Expr("age >= ?", 30))
	if err == nil {
		t.Fatal("duplicate where slot must be rejected")
	}
	e := AsError(err)
	if e.Kind != KindWhere || e.Field != "age" || !strings.Contains(e.Reason, "conflicting conditions") {
		t.Fatalf("where conflict error: %+v", e)
	}
	// between 拆占 gte + lte:与独立 gte 的重叠同样被拦截。
	if err := st.Where("age", OpLte, sq.Expr("age <= ?", 40)); err != nil {
		t.Fatalf("distinct slot must pass: %v", err)
	}
	if err := st.Where("age", OpGte, sq.Expr("age >= ?", 25)); err == nil {
		t.Fatal("between gte slot collision must be rejected")
	}
	if len(st.Wheres()) != 2 {
		t.Fatal("rejected where must not be appended")
	}
}

// ---- 校验边界 ----

func TestUpdateValidate(t *testing.T) {
	// 空 SET → KindSet。
	st := newTestUpdateState()
	if err := st.Where("id", OpEq, sq.Expr("id = ?", 1)); err != nil {
		t.Fatal(err)
	}
	e := AsError(st.Validate())
	if e.Kind != KindSet || !strings.Contains(e.Reason, "empty set") {
		t.Fatalf("empty set: %+v", e)
	}

	// 有 SET 但零 where → KindWhere。
	st = newTestUpdateState()
	if err := st.Set("name", "name", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	e = AsError(st.Validate())
	if e.Kind != KindWhere || !strings.Contains(e.Reason, "zero where") {
		t.Fatalf("zero where: %+v", e)
	}

	// 零 where 守卫按调用方条件数计:@filter 谓词不在 state 内,恒定叠加后
	// 最终 WHERE 恒非空也不得豁免。固定条件的生成期表达式不参与计数——
	// 仅 Set 的 state 仍须拒绝。
	st = newTestUpdateState()
	if err := st.Set("name", "name", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err == nil {
		t.Fatal("@filter conditions must not exempt the caller zero-where guard")
	}
}

// TestUpdateNullOpsConflict 覆盖 is_null / not_null 同列互斥(与 List 一致):
// 两个条件 AND 后恒空,静默 0 行不如显式拒绝。
func TestUpdateNullOpsConflict(t *testing.T) {
	st := newTestUpdateState()
	if err := st.Where("deletedAt", OpIsNull, sq.Expr("deleted_at IS NULL")); err != nil {
		t.Fatal(err)
	}
	err := st.Where("deletedAt", OpNotNull, sq.Expr("deleted_at IS NOT NULL"))
	if err == nil {
		t.Fatal("is_null + not_null on the same field must be rejected")
	}
	e := AsError(err)
	if e.Kind != KindWhere || e.Field != "deletedAt" || !strings.Contains(e.Reason, "conflicting conditions") {
		t.Fatalf("null-op conflict error: %+v", e)
	}
	if len(st.Wheres()) != 1 {
		t.Fatal("rejected where must not be appended")
	}
}

// TestUpdateMutationErrorHelpers 覆盖生成代码引用的构造器错误形态。
func TestUpdateMutationErrorHelpers(t *testing.T) {
	if e := ErrEmptyPattern("name"); e.Kind != KindWhere || e.Field != "name" || !strings.Contains(e.Reason, "empty pattern") {
		t.Fatalf("empty pattern error: %+v", e)
	}
	if e := ErrNilSet("tags"); e.Kind != KindSet || e.Field != "tags" || !strings.Contains(e.Reason, "nil value") {
		t.Fatalf("nil set error: %+v", e)
	}
}

func TestUpdateCheckInLen(t *testing.T) {
	if err := CheckInLen("id", 1); err != nil {
		t.Fatalf("single element: %v", err)
	}
	if err := CheckInLen("id", MaxIn); err != nil {
		t.Fatalf("MaxIn elements: %v", err)
	}
	for _, n := range []int{0, MaxIn + 1} {
		err := CheckInLen("id", n)
		if err == nil {
			t.Fatalf("length %d must be rejected", n)
		}
		e := AsError(err)
		if e.Kind != KindWhere || e.Field != "id" {
			t.Fatalf("length %d error: %+v", n, e)
		}
	}
}

func TestUpdateErrBetweenOrder(t *testing.T) {
	e := ErrBetweenOrder("age")
	if e.Kind != KindWhere || e.Field != "age" || !strings.Contains(e.Reason, "lower bound exceeds upper bound") {
		t.Fatalf("between order error: %+v", e)
	}
}

func TestUpdateFilterBypass(t *testing.T) {
	st := newTestUpdateState()
	if st.FilterBypassed() {
		t.Fatal("bypass must default to false")
	}
	st.BypassFilter()
	st.BypassFilter() // 幂等
	if !st.FilterBypassed() {
		t.Fatal("bypass flag")
	}
	// 绕过只影响 @filter 不变量,与零 where 守卫正交:仅 Set + bypass 仍被拒绝。
	if err := st.Set("name", "name", "", "Alice"); err != nil {
		t.Fatal(err)
	}
	e := AsError(st.Validate())
	if e.Kind != KindWhere || !strings.Contains(e.Reason, "zero where") {
		t.Fatalf("bypass must not exempt zero-where guard: %+v", e)
	}
	if err := st.Where("id", OpEq, sq.Expr("id = ?", 1)); err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("Validate after where: %v", err)
	}
}

func TestPtr(t *testing.T) {
	p := Ptr("x")
	if p == nil || *p != "x" {
		t.Fatalf("Ptr: %v", p)
	}
	if Ptr(1) == Ptr(1) {
		t.Fatal("Ptr must return distinct pointers per call")
	}
}
