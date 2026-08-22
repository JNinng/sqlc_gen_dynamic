package dynq

import (
	"strings"
	"testing"
)

// ---- QueryOption:集合语义(幂等、交换、去重)与未知目标 ----

func TestQueryOptionSetSemantics(t *testing.T) {
	meta := testMeta()
	cfg, err := NewQueryConfig(meta,
		DisableWhere("name"), DisableWhere("name"), // 幂等
		DisableSort("id", "name"), DisableSort("name", "id"), // 交换、去重
	)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WhereDisabled("name") || cfg.WhereDisabled("id") {
		t.Fatal("where disable set")
	}
	if !cfg.SortDisabled("id") || !cfg.SortDisabled("name") || cfg.SortDisabled("score") {
		t.Fatal("sort disable set")
	}
	if cfg.SelectDisabled("name") {
		t.Fatal("select untouched")
	}
	// nil option 忽略
	if _, err := NewQueryConfig(meta, nil); err != nil {
		t.Fatal(err)
	}
}

func TestQueryOptionUnknownTarget(t *testing.T) {
	meta := testMeta()
	_, err := NewQueryConfig(meta, DisableWhere("ghost"))
	if err == nil {
		t.Fatal("unknown target rejected")
	}
	e := AsError(err)
	if e.Kind != KindOption || e.Field != "ghost" {
		t.Fatalf("option error: %+v", e)
	}
	// 跨表误用
	_, err = NewQueryConfig(meta, DisableSort("orderNo"))
	if err == nil {
		t.Fatal("cross-table target rejected")
	}
}

// ---- Slots:条件冲突检测 ----

func TestSlotsConflict(t *testing.T) {
	s := NewSlots()
	if err := s.Take("score.gte", 0, "score"); err != nil {
		t.Fatal(err)
	}
	err := s.Take("score.gte", 1, "score") // 独立 gte 与 between 拆分后的 gte 重叠
	if err == nil {
		t.Fatal("conflict detected")
	}
	e := AsError(err)
	if e.Kind != KindWhere || e.Index != 1 || !strings.Contains(e.Reason, "conflicting") {
		t.Fatalf("conflict error: %+v", e)
	}
	// 不同目标不冲突
	if err := s.Take("score.lte", 2, "score"); err != nil {
		t.Fatal(err)
	}
}

// ---- Budget:占位符预算 ----

func TestBudget(t *testing.T) {
	b := NewBudget()
	for i := 0; i < 59999; i++ {
		if err := b.Add(1); err != nil {
			t.Fatalf("premature: %v", err)
		}
	}
	if err := b.Add(1); err != nil {
		t.Fatalf("exactly at budget must pass: %v", err)
	}
	if err := b.Add(1); err == nil {
		t.Fatal("over budget rejected")
	}
}

// ---- ParseOp ----

func TestParseOp(t *testing.T) {
	if op, err := ParseOp("eq"); err != nil || op != OpEq {
		t.Fatalf("eq: %v %v", op, err)
	}
	if _, err := ParseOp("=~"); err == nil {
		t.Fatal("unknown op rejected")
	}
}
