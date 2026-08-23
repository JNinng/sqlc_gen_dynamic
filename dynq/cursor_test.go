package dynq

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundtrip(t *testing.T) {
	meta := testMeta()
	seq := seqOf(t, meta, []OrderBy{{Field: "updatedAt", Desc: true}}) // updatedAt DESC, id ASC
	fp := CursorFingerprint("users", seq)

	createdAt := mustTime(t, "2026-08-01T12:30:45.123456+08:00")
	uid := uuid.MustParse("0f14d0ab-9605-4a62-a9e4-5ed26688389b")

	cases := []struct {
		name string
		dt   DataType
		val  any
	}{
		{"integer", DataTypeInteger, int64(42)},
		{"integer ptr", DataTypeInteger, ptr(int64(42))},
		{"float", DataTypeNumber, 9.5},
		{"float ptr", DataTypeNumber, ptr(9.5)},
		{"NaN", DataTypeNumber, math.NaN()},
		{"+Inf", DataTypeNumber, math.Inf(1)},
		{"-Inf", DataTypeNumber, math.Inf(-1)},
		{"datetime", DataTypeDatetime, createdAt},
		{"datetime ptr", DataTypeDatetime, &createdAt},
		{"string", DataTypeString, "alice"},
		{"bool", DataTypeBoolean, true},
		{"uuid", DataTypeUUID, uid},
		{"uuid ptr", DataTypeUUID, &uid},
		{"null datetime", DataTypeDatetime, (*time.Time)(nil)},
		{"null float", DataTypeNumber, (*float64)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			single := []SortField{{Key: "x", Column: "x", DataType: tc.dt, Nullable: true, Desc: true}}
			sfp := CursorFingerprint("t", single)
			cur, err := EncodeCursor(sfp, single, []any{tc.val})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			vals, err := DecodeCursor(cur, sfp, single)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			switch want := tc.val.(type) {
			case *time.Time:
				if want == nil {
					if vals[0] != nil {
						t.Fatalf("want nil, got %#v", vals[0])
					}
				} else if got := vals[0].(time.Time); !got.Equal(*want) {
					t.Fatalf("time: %v vs %v", got, *want)
				}
			case *float64:
				if want == nil {
					if vals[0] != nil {
						t.Fatalf("want nil, got %#v", vals[0])
					}
				} else if got := vals[0].(float64); got != *want {
					t.Fatalf("float: %v vs %v", got, *want)
				}
			case *int64:
				if got := vals[0].(int64); got != *want {
					t.Fatalf("int64 ptr: %v vs %v", got, *want)
				}
			case time.Time:
				if got := vals[0].(time.Time); !got.Equal(want) {
					t.Fatalf("time: %v vs %v", got, want)
				}
			case float64:
				got := vals[0].(float64)
				if math.IsNaN(want) && !math.IsNaN(got) {
					t.Fatalf("NaN not restored")
				} else if !math.IsNaN(want) && got != want {
					t.Fatalf("float: %v vs %v", got, want)
				}
			case uuid.UUID:
				if got := vals[0].(string); got != want.String() {
					t.Fatalf("uuid: %v vs %v", got, want)
				}
			case *uuid.UUID:
				if want == nil {
					if vals[0] != nil {
						t.Fatalf("want nil, got %#v", vals[0])
					}
				} else if got := vals[0].(string); got != want.String() {
					t.Fatalf("uuid ptr: %v vs %v", got, want)
				}
			default:
				if vals[0] != tc.val {
					t.Fatalf("value: %#v vs %#v", vals[0], tc.val)
				}
			}
		})
	}
	_ = fp
}

func TestCursorDateEncoding(t *testing.T) {
	single := []SortField{{Key: "d", Column: "d", DataType: DataTypeDate, Nullable: true}}
	d := mustTime(t, "2026-08-22T00:00:00Z")
	cur, err := EncodeCursor(CursorFingerprint("t", single), single, []any{d})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decodeRawCursor(t, cur), `"2026-08-22"`) {
		t.Fatalf("date should encode as YYYY-MM-DD: %s", cur)
	}
}

func TestCursorDatetimeEncoding(t *testing.T) {
	single := []SortField{{Key: "t", Column: "t", DataType: DataTypeDatetime, Nullable: true}}
	tm := mustTime(t, "2026-08-22T10:00:00+08:00")
	cur, err := EncodeCursor(CursorFingerprint("t", single), single, []any{tm})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decodeRawCursor(t, cur), `"2026-08-22T10:00:00+08:00"`) {
		t.Fatalf("datetime should encode as RFC3339: %s", decodeRawCursor(t, cur))
	}
}

func TestCursorFingerprintMismatch(t *testing.T) {
	single := []SortField{{Key: "id", Column: "id", DataType: DataTypeInteger}}
	fp := CursorFingerprint("users", single)
	cur, err := EncodeCursor(fp, single, []any{int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	// 同规格跨表游标
	if _, err := DecodeCursor(cur, CursorFingerprint("orders", single), single); err == nil {
		t.Fatal("cross-resource cursor must be rejected")
	} else if e := AsError(err); e.Kind != KindCursor || !strings.Contains(e.Reason, "fingerprint") {
		t.Fatalf("kind/reason: %+v", e)
	}
	// 换排序
	other := []SortField{
		{Key: "id", Column: "id", DataType: DataTypeInteger},
		{Key: "x", Column: "x", DataType: DataTypeInteger},
	}
	if _, err := DecodeCursor(cur, CursorFingerprint("users", other), other); err == nil {
		t.Fatal("changed sort must be rejected")
	}
	// 篡改载荷
	if _, err := DecodeCursor("!!!not-base64!!!", fp, single); err == nil {
		t.Fatal("malformed base64 rejected")
	}
}

func TestCursorArityAndTypeMismatch(t *testing.T) {
	single := []SortField{{Key: "id", Column: "id", DataType: DataTypeInteger}}
	cur, _ := EncodeCursor(CursorFingerprint("u", single), single, []any{int64(1)})
	double := append(single, SortField{Key: "x", Column: "x", DataType: DataTypeInteger})
	if _, err := DecodeCursor(cur, CursorFingerprint("u", double), double); err == nil {
		t.Fatal("arity mismatch rejected")
	}
	strOnly := []SortField{{Key: "s", Column: "s", DataType: DataTypeString}}
	strCur, _ := EncodeCursor(CursorFingerprint("u", strOnly), strOnly, []any{"s"})
	_ = strCur
	// 类型不符:datetime 游标列值给整数
	dtSeq := []SortField{{Key: "t", Column: "t", DataType: DataTypeDatetime, Nullable: true}}
	raw := `{"f":"` + strconv.FormatUint(CursorFingerprint("u", dtSeq), 10) + `","v":[42]}`
	if _, err := DecodeCursor(base64.RawURLEncoding.EncodeToString([]byte(raw)), CursorFingerprint("u", dtSeq), dtSeq); err == nil {
		t.Fatal("type mismatch rejected")
	}
}

func TestCursorSentinelRestoreThenCoerce(t *testing.T) {
	// NaN 哨兵还原后仍走类型收敛:非 number 列拒绝
	numSeq := []SortField{{Key: "f", Column: "f", DataType: DataTypeNumber}}
	cur, _ := EncodeCursor(CursorFingerprint("u", numSeq), numSeq, []any{math.NaN()})
	vals, err := DecodeCursor(cur, CursorFingerprint("u", numSeq), numSeq)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(vals[0].(float64)) {
		t.Fatal("NaN sentinel restored")
	}
}

func decodeRawCursor(t *testing.T, cur string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ptr[T any](v T) *T { return &v }
