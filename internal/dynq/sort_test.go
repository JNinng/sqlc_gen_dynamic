package dynq

import (
	"testing"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// testMeta 是覆盖多数 dataType 与形态的 registry 测试基准。
func testMeta() TableMeta {
	return TableMeta{
		Resource: "users",
		Table:    "users",
		Fields: []FieldMeta{
			{Key: "id", Column: "id", DataType: DataTypeInteger, Sortable: true, Selectable: true, Label: "ID"},
			{Key: "name", Column: "name", DataType: DataTypeString, Ops: []Op{OpEq, OpContains, OpPrefix}, Sortable: true, Selectable: true, Label: "姓名"},
			{Key: "score", Column: "score", DataType: DataTypeNumber, Nullable: true, Ops: []Op{OpGte, OpLte, OpBetween}, Sortable: true},
			{Key: "updatedAt", Column: "updated_at", DataType: DataTypeDatetime, Nullable: true, Ops: []Op{OpGte, OpIsNull, OpNotNull}, Sortable: true},
			{Key: "createdAt", Column: "created_at", DataType: DataTypeDatetime, Ops: []Op{OpBetween, OpGte}, Sortable: true},
			{Key: "status", Column: "status", DataType: DataTypeEnum, Ops: []Op{OpEq, OpIn}, Sortable: true, Enum: []string{"active", "banned"}},
			{Key: "password", Column: "password", DataType: DataTypeString, Hidden: true},
		},
		DefaultSort: []OrderBy{{Field: "createdAt", Desc: true}},
		Tiebreaker:  "id",
		PageDefault: 20,
		PageMax:     200,
	}
}

func sqlize(t *testing.T, b sq.Sqlizer) string {
	t.Helper()
	sqlStr, _, err := b.ToSql()
	if err != nil {
		t.Fatalf("ToSql: %v", err)
	}
	return sqlStr
}

func TestResolveSort(t *testing.T) {
	meta := testMeta()
	cfg, err := NewQueryConfig(meta)
	if err != nil {
		t.Fatal(err)
	}

	// 用户排序 + tiebreaker
	seq, err := ResolveSort(meta, cfg, []OrderBy{{Field: "name", Desc: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seq) != 2 || seq[0].Key != "name" || !seq[0].Desc || seq[1].Key != "id" || seq[1].Desc {
		t.Fatalf("seq: %+v", seq)
	}

	// 缺省回落 default_sort + tiebreaker
	seq, err = ResolveSort(meta, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seq) != 2 || seq[0].Key != "createdAt" || !seq[0].Desc || seq[1].Key != "id" {
		t.Fatalf("default: %+v", seq)
	}

	// 末列已为主键则不重复追加
	seq, err = ResolveSort(meta, cfg, []OrderBy{{Field: "id"}})
	if err != nil || len(seq) != 1 {
		t.Fatalf("id last: %+v %v", seq, err)
	}

	// 不可排序 / 未知 / hidden / 禁用
	if _, err := ResolveSort(meta, cfg, []OrderBy{{Field: "ghost"}}); err == nil {
		t.Fatal("unknown sort field rejected")
	}
	if _, err := ResolveSort(meta, cfg, []OrderBy{{Field: "statusX"}}); err == nil {
		t.Fatal("unknown rejected")
	}
	disabled, _ := NewQueryConfig(meta, DisableSort("name"))
	if _, err := ResolveSort(meta, disabled, []OrderBy{{Field: "name"}}); err == nil {
		t.Fatal("disabled sort field rejected")
	} else if e := AsError(err); e.Kind != KindSort || e.Reason != "field disabled in this context" {
		t.Fatalf("reason: %+v", e)
	}

	// 无 default_sort 的表:纯 tiebreaker
	noDefault := testMeta()
	noDefault.DefaultSort = nil
	seq, err = ResolveSort(noDefault, cfg, nil)
	if err != nil || len(seq) != 1 || seq[0].Key != "id" {
		t.Fatalf("tiebreaker only: %+v %v", seq, err)
	}
}

func TestOrderBySQLNullsPlacement(t *testing.T) {
	meta := testMeta()
	cfg, _ := NewQueryConfig(meta)
	seq, _ := ResolveSort(meta, cfg, []OrderBy{{Field: "createdAt", Desc: true}})
	got := OrderBySQL(seq)
	want := "created_at DESC NULLS FIRST, id ASC NULLS LAST"
	if got != want {
		t.Fatalf("order by:\n got %q\nwant %q", got, want)
	}
}

// ---- seek 谓词快照 ----

func seqOf(t *testing.T, meta TableMeta, order []OrderBy) []SortField {
	t.Helper()
	cfg, _ := NewQueryConfig(meta)
	seq, err := ResolveSort(meta, cfg, order)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestSeekPredicateSingleColumnPlusTiebreaker(t *testing.T) {
	meta := testMeta()
	seq := seqOf(t, meta, []OrderBy{{Field: "createdAt", Desc: true}})
	p, err := SeekPredicate(seq, []any{mustTime(t, "2026-08-01T00:00:00Z"), int64(42)})
	if err != nil {
		t.Fatal(err)
	}
	got := sqlize(t, p)
	want := "((created_at < ?) OR (created_at = ? AND id > ?))"
	if got != want {
		t.Fatalf("seek:\n got %q\nwant %q", got, want)
	}
}

func TestSeekPredicateMixedDirections(t *testing.T) {
	meta := testMeta()
	seq := seqOf(t, meta, []OrderBy{{Field: "name", Desc: false}, {Field: "score", Desc: true}})
	p, err := SeekPredicate(seq, []any{"alice", 9.5, int64(7)})
	if err != nil {
		t.Fatal(err)
	}
	got := sqlize(t, p)
	// name 非空 → 普通 =;score 可空 → IS NOT DISTINCT FROM;id tiebreaker 恒 ASC → >
	want := "(((name > ? OR name IS NULL)) OR (name = ? AND score < ?) OR (name = ? AND score IS NOT DISTINCT FROM ? AND id > ?))"
	if got != want {
		t.Fatalf("seek mixed:\n got %q\nwant %q", got, want)
	}
}

func TestSeekPredicateNullableNULLBranches(t *testing.T) {
	meta := testMeta()

	// DESC(NULLS FIRST) + 游标值 NULL → IS NOT NULL
	seq := seqOf(t, meta, []OrderBy{{Field: "score", Desc: true}})
	p, _ := SeekPredicate(seq, []any{nil, int64(7)})
	if got, want := sqlize(t, p), "((score IS NOT NULL) OR (score IS NOT DISTINCT FROM ? AND id > ?))"; got != want {
		t.Fatalf("desc null:\n got %q\nwant %q", got, want)
	}

	// ASC(NULLS LAST) + 游标值 NULL → 整项模板期消除,仅剩 tiebreaker 项
	seq = seqOf(t, meta, []OrderBy{{Field: "score", Desc: false}})
	p, _ = SeekPredicate(seq, []any{nil, int64(7)})
	if got, want := sqlize(t, p), "(score IS NOT DISTINCT FROM ? AND id > ?)"; got != want {
		t.Fatalf("asc null:\n got %q\nwant %q", got, want)
	}

	// ASC 非 NULL → x > v OR x IS NULL
	p, _ = SeekPredicate(seq, []any{9.5, int64(7)})
	if got, want := sqlize(t, p),
		"(((score > ? OR score IS NULL)) OR (score IS NOT DISTINCT FROM ? AND id > ?))"; got != want {
		t.Fatalf("asc non-null:\n got %q\nwant %q", got, want)
	}

	// 首列游标值为 NULL 的 ASC:全组消除后仍可能有第二项(INDISTINCT NULL 匹配 NULL 组)
	// 单列排序 + ASC + NULL → 全部消除 → false
	single := seqOf(t, meta, []OrderBy{{Field: "score", Desc: false}})
	// 长度 2(score,id);构造 score 为 NULL 且 id 也为 NULL 的场景不存在(id 非空),
	// 这里直接验证 id NULL 的防御路径:last col NULL → false
	p, _ = SeekPredicate(single, []any{9.5, nil})
	if got := sqlize(t, p); got != "false" {
		t.Fatalf("last null defends as false, got %q", got)
	}
}

func TestSeekPredicateArity(t *testing.T) {
	meta := testMeta()
	seq := seqOf(t, meta, nil)
	if _, err := SeekPredicate(seq, []any{int64(1)}); err == nil {
		t.Fatal("arity mismatch rejected")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// ---- 排序指纹 ----

func TestCursorFingerprint(t *testing.T) {
	meta := testMeta()
	seqA := seqOf(t, meta, nil) // createdAt DESC, id ASC
	// 同资源同排序 → 相同指纹
	if CursorFingerprint("users", seqA) != CursorFingerprint("users", seqOf(t, meta, nil)) {
		t.Fatal("same resource+sort must share fingerprint")
	}
	// 跨表同规格排序(orders 也有 createdAt DESC, id)→ 指纹不同(掺资源名)
	if CursorFingerprint("users", seqA) == CursorFingerprint("orders", seqA) {
		t.Fatal("cross-resource same sort spec must differ")
	}
	// 换排序 → 指纹不同
	seqB := seqOf(t, meta, []OrderBy{{Field: "name"}})
	if CursorFingerprint("users", seqA) == CursorFingerprint("users", seqB) {
		t.Fatal("different sort must differ")
	}
	// 方向不同 → 指纹不同
	seqC := seqOf(t, meta, []OrderBy{{Field: "createdAt"}})
	if CursorFingerprint("users", seqA) == CursorFingerprint("users", seqC) {
		t.Fatal("direction must matter")
	}
}
