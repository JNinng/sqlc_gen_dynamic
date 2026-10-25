package dynq

import (
	"encoding/base64"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
		{"int16", DataTypeInteger, int16(42)},
		{"int16 ptr", DataTypeInteger, ptr(int16(42))},
		{"null int16", DataTypeInteger, (*int16)(nil)},
		{"float", DataTypeNumber, 9.5},
		{"float ptr", DataTypeNumber, ptr(9.5)},
		{"float32", DataTypeNumber, float32(9.5)},
		{"float32 ptr", DataTypeNumber, ptr(float32(9.5))},
		{"float32 NaN", DataTypeNumber, float32(math.NaN())},
		{"float32 +Inf", DataTypeNumber, float32(math.Inf(1))},
		{"null float32", DataTypeNumber, (*float32)(nil)},
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
			case *int16:
				if want == nil {
					if vals[0] != nil {
						t.Fatalf("want nil, got %#v", vals[0])
					}
				} else if got := vals[0].(int64); got != int64(*want) {
					t.Fatalf("int16 ptr: %v vs %v", got, *want)
				}
			case int16:
				if got := vals[0].(int64); got != int64(want) {
					t.Fatalf("int16: %v vs %v", got, want)
				}
			case *float32:
				if want == nil {
					if vals[0] != nil {
						t.Fatalf("want nil, got %#v", vals[0])
					}
				} else if got := vals[0].(float64); got != float64(*want) {
					t.Fatalf("float32 ptr: %v vs %v", got, *want)
				}
			case float32:
				got := vals[0].(float64)
				if math.IsNaN(float64(want)) && !math.IsNaN(got) {
					t.Fatalf("NaN not restored")
				} else if !math.IsNaN(float64(want)) && got != float64(want) {
					t.Fatalf("float32: %v vs %v", got, want)
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

// hugeInt 构造超出 int64 的十进制整数(测试辅助)。
func hugeInt(t *testing.T, decimal string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(decimal, 10)
	if !ok {
		t.Fatalf("bad big int literal %q", decimal)
	}
	return n
}

// TestCursorNumericEncoding 覆盖 numeric 列的游标编码:有限值保留标度的十进制
// 字符串(Int × 10^Exp,含超 float64 精度与标度差异形态),NaN/±Inf 复用 number
// 哨兵,NULL 编码为 JSON null;解码按 DataTypeDecimal 保留字符串。
func TestCursorNumericEncoding(t *testing.T) {
	cases := []struct {
		name string
		n    pgtype.Numeric
		want any // 解码后的游标列值
	}{
		{"finite", pgtype.Numeric{Int: big.NewInt(15), Exp: -1, Valid: true}, "1.5"},
		{"scale preserved", pgtype.Numeric{Int: big.NewInt(150), Exp: -2, Valid: true}, "1.50"},
		{"negative", pgtype.Numeric{Int: big.NewInt(-125), Exp: -2, Valid: true}, "-1.25"},
		{"positive exponent", pgtype.Numeric{Int: big.NewInt(15), Exp: 2, Valid: true}, "1500"},
		{"zero exponent", pgtype.Numeric{Int: big.NewInt(42), Exp: 0, Valid: true}, "42"},
		{"leading fraction", pgtype.Numeric{Int: big.NewInt(123), Exp: -6, Valid: true}, "0.000123"},
		{"pgx zero (nil int)", pgtype.Numeric{Valid: true}, "0"},
		{"zero with scale", pgtype.Numeric{Int: big.NewInt(0), Exp: -2, Valid: true}, "0.00"},
		{"beyond float64 precision", pgtype.Numeric{Int: hugeInt(t, "1234567890123456789012345678901234567890"), Exp: -10, Valid: true}, "123456789012345678901234567890.1234567890"},
		{"negative beyond float64 precision", pgtype.Numeric{Int: hugeInt(t, "-98765432109876543210987654321098765432109"), Exp: -10, Valid: true}, "-9876543210987654321098765432109.8765432109"},
		{"NaN", pgtype.Numeric{NaN: true, Valid: true}, sentinelNaN},
		{"+Inf", pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true}, sentinelPosInf},
		{"-Inf", pgtype.Numeric{InfinityModifier: pgtype.NegativeInfinity, Valid: true}, sentinelNegInf},
		{"NULL", pgtype.Numeric{Valid: false}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			single := []SortField{{Key: "b", Column: "balance", DataType: DataTypeDecimal, Nullable: true}}
			cur, err := EncodeCursor(CursorFingerprint("t", single), single, []any{tc.n})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			vals, err := DecodeCursor(cur, CursorFingerprint("t", single), single)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if want, ok := tc.want.(string); ok {
				if got := vals[0].(string); got != want {
					t.Fatalf("value:\n got %q\nwant %q", got, want)
				}
			} else if vals[0] != nil {
				t.Fatalf("want nil, got %#v", vals[0])
			}
		})
	}
}

// TestCursorNumericPointerEncoding 覆盖 *pgtype.Numeric(go_type_overrides 或
// 驱动形态差异)与 nil 指针的编码路径。
func TestCursorNumericPointerEncoding(t *testing.T) {
	single := []SortField{{Key: "b", Column: "balance", DataType: DataTypeDecimal, Nullable: true}}
	n := pgtype.Numeric{Int: big.NewInt(15), Exp: -1, Valid: true}
	cur, err := EncodeCursor(CursorFingerprint("t", single), single, []any{&n})
	if err != nil {
		t.Fatal(err)
	}
	vals, err := DecodeCursor(cur, CursorFingerprint("t", single), single)
	if err != nil {
		t.Fatal(err)
	}
	if got := vals[0].(string); got != "1.5" {
		t.Fatalf("pointer numeric: got %q", got)
	}

	cur, err = EncodeCursor(CursorFingerprint("t", single), single, []any{(*pgtype.Numeric)(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if vals, err = DecodeCursor(cur, CursorFingerprint("t", single), single); err != nil {
		t.Fatal(err)
	}
	if vals[0] != nil {
		t.Fatalf("nil pointer must encode to null, got %#v", vals[0])
	}
}

// TestCursorDecimalDecodeStrict 覆盖 decimal 解码的严格性:游标值只能出自
// EncodeCursor 的字符串形态,JSON number / 非法字面量 / 指数形态一律拒绝;
// 合法字面量(含哨兵)原样透传,标度差异由服务端 numeric 等值判定吸收。
func TestCursorDecimalDecodeStrict(t *testing.T) {
	single := []SortField{{Key: "b", Column: "balance", DataType: DataTypeDecimal, Nullable: true}}
	fp := CursorFingerprint("u", single)
	rawCursor := func(v string) string {
		raw := `{"f":"` + strconv.FormatUint(fp, 10) + `","v":[` + v + `]}`
		return base64.RawURLEncoding.EncodeToString([]byte(raw))
	}

	// 拒绝路径:Kind 恒为 cursor,Reason 区分"形态不符"与"字面量非法";
	// 逐 payload 独立报告,不因首个失败中止余下用例。
	for _, tc := range []struct{ payload, reason string }{
		{`1.5`, "expected decimal string"}, // JSON number 形态
		{`true`, "expected decimal string"},
		{`"abc"`, "invalid decimal"},
		{`"1e10"`, "invalid decimal"}, // 指数形态编码器不产出
		{`""`, "invalid decimal"},
		{`"1.5.0"`, "invalid decimal"},
	} {
		_, err := DecodeCursor(rawCursor(tc.payload), fp, single)
		if err == nil {
			t.Errorf("decimal decode must reject payload %s", tc.payload)
			continue
		}
		if e := AsError(err); e.Kind != KindCursor || !strings.Contains(e.Reason, tc.reason) {
			t.Errorf("payload %s: kind/reason = %+v, want kind=cursor reason~%q", tc.payload, e, tc.reason)
		}
	}
	for _, tc := range []struct{ payload, want string }{
		{`"1.50"`, "1.50"},
		{`"-0.00"`, "-0.00"},
		{`"NaN"`, sentinelNaN},
		{`"Infinity"`, sentinelPosInf},
		{`"-Infinity"`, sentinelNegInf},
	} {
		vals, err := DecodeCursor(rawCursor(tc.payload), fp, single)
		if err != nil {
			t.Fatalf("decode %s: %v", tc.payload, err)
		}
		if got := vals[0].(string); got != tc.want {
			t.Fatalf("decode %s:\n got %q\nwant %q", tc.payload, got, tc.want)
		}
	}
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

func TestEncodeCursorUnsupportedTypeFails(t *testing.T) {
	single := []SortField{{Key: "x", Column: "x", DataType: DataTypeString}}
	_, err := EncodeCursor(CursorFingerprint("u", single), single, []any{struct{ A int }{A: 1}})
	if err == nil {
		t.Fatal("unsupported type must be rejected")
	}
	if e := AsError(err); e.Kind != KindCursor || !strings.Contains(e.Reason, "cannot encode cursor value") {
		t.Fatalf("kind/reason: %+v", e)
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
