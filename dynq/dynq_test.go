package dynq

import (
	"strings"
	"testing"
)

func wantErrContaining(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("want error containing %q, got %q", substr, err.Error())
	}
}

// ---- 类型收敛 ----

func TestAsInt64(t *testing.T) {
	if v, err := AsInt64(float64(42)); err != nil || v != 42 {
		t.Fatalf("42: %v %v", v, err)
	}
	// Go 原生整数类型(typed 构造器直填 Query 的场景)。
	if v, err := AsInt64(int64(42)); err != nil || v != 42 {
		t.Fatalf("int64 42: %v %v", v, err)
	}
	if v, err := AsInt64(int(42)); err != nil || v != 42 {
		t.Fatalf("int 42: %v %v", v, err)
	}
	if _, err := AsInt64("42"); err == nil {
		t.Fatal("string number must be rejected")
	}
	if _, err := AsInt64(1.5); err == nil {
		t.Fatal("non-integer must be rejected")
	}
	if _, err := AsInt64(nil); err == nil {
		t.Fatal("null must be rejected with is_null hint")
	} else if !strings.Contains(err.Error(), "is_null") {
		t.Fatalf("null rejection should hint is_null, got %v", err)
	}
	if _, err := AsInt64(true); err == nil {
		t.Fatal("bool must be rejected")
	}
}

func TestAsFloat64(t *testing.T) {
	if v, err := AsFloat64(1.25); err != nil || v != 1.25 {
		t.Fatalf("1.25: %v %v", v, err)
	}
	if _, err := AsFloat64("1.25"); err == nil {
		t.Fatal("string number must be rejected")
	}
	if _, err := AsFloat64(nil); err == nil {
		t.Fatal("null rejected")
	}
}

func TestAsStringAndBool(t *testing.T) {
	if v, err := AsString("abc"); err != nil || v != "abc" {
		t.Fatalf("abc: %v %v", v, err)
	}
	if _, err := AsString(1); err == nil {
		t.Fatal("number as string rejected")
	}
	if v, err := AsBool(true); err != nil || !v {
		t.Fatalf("true: %v %v", v, err)
	}
	if _, err := AsBool("true"); err == nil {
		t.Fatal("string bool rejected")
	}
}

func TestAsTimeAndDate(t *testing.T) {
	if _, err := AsTime("2026-08-22T10:00:00+08:00"); err != nil {
		t.Fatalf("RFC3339 accepted: %v", err)
	}
	if _, err := AsTime("2026-08-22 10:00:00"); err == nil {
		t.Fatal("non-RFC3339 rejected")
	}
	if _, err := AsTime("2026-08-22"); err == nil {
		t.Fatal("date string is not RFC3339 datetime")
	}
	if _, err := AsDate("2026-08-22"); err != nil {
		t.Fatalf("date accepted: %v", err)
	}
	if _, err := AsDate("2026-08-22T10:00:00Z"); err == nil {
		t.Fatal("RFC3339 is not a date")
	}
}

func TestAsUUID(t *testing.T) {
	if v, err := AsUUID("0f14d0ab-9605-4a62-a9e4-5ed26688389b"); err != nil ||
		v != "0f14d0ab-9605-4a62-a9e4-5ed26688389b" {
		t.Fatalf("uuid: %v %v", v, err)
	}
	if _, err := AsUUID("not-a-uuid"); err == nil {
		t.Fatal("invalid uuid rejected")
	}
}

func TestAsEnum(t *testing.T) {
	vals := []string{"active", "banned"}
	if v, err := AsEnum(vals, "active"); err != nil || v != "active" {
		t.Fatalf("enum: %v %v", v, err)
	}
	if _, err := AsEnum(vals, "ghost"); err == nil {
		t.Fatal("unknown enum value rejected")
	}
	if _, err := AsEnum(vals, nil); err == nil {
		t.Fatal("null enum rejected")
	}
}

func TestAsJSON(t *testing.T) {
	b, err := AsJSON(map[string]any{"a": float64(1)})
	if err != nil || string(b) != `{"a":1}` {
		t.Fatalf("json: %s %v", b, err)
	}
	if _, err := AsJSON(nil); err == nil {
		t.Fatal("null json rejected")
	}
}

func TestSlices(t *testing.T) {
	if v, err := AsInt64Slice([]any{float64(1), float64(2)}); err != nil || len(v) != 2 {
		t.Fatalf("int slice: %v %v", v, err)
	}
	if _, err := AsInt64Slice([]any{}); err == nil {
		t.Fatal("empty array rejected")
	}
	if _, err := AsInt64Slice([]any{"1"}); err == nil {
		t.Fatal("string element rejected")
	}
	if _, err := AsInt64Slice(3.14); err == nil {
		t.Fatal("non-array rejected")
	}
	big := make([]any, MaxIn+1)
	for i := range big {
		big[i] = float64(i)
	}
	if _, err := AsInt64Slice(big); err == nil {
		t.Fatal("array over MaxIn rejected")
	}
}

// ---- 模糊三 op 转义 ----

func TestEscapeLike(t *testing.T) {
	if got := EscapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("escape: %q", got)
	}
	if got := ContainsPattern("a%b"); got != `%a\%b%` {
		t.Fatalf("contains: %q", got)
	}
	if got := PrefixPattern("a_b"); got != `a\_b%` {
		t.Fatalf("prefix: %q", got)
	}
	if got := SuffixPattern(`x\y`); got != `%x\\y` {
		t.Fatalf("suffix: %q", got)
	}
}

// ---- NormalizeOrder ----

func TestNormalizeOrder(t *testing.T) {
	for in, want := range map[string]bool{"asc": false, "ASC": false, "Desc": true, "DESC": true} {
		got, err := NormalizeOrder(in)
		if err != nil || got != want {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
	if _, err := NormalizeOrder("ascending"); err == nil {
		t.Fatal("invalid order rejected")
	}
}
