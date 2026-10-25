package dynq

import (
	"testing"
	"time"
)

// 切片收敛族直测。共享路径(nil 提示、空数组、超 MaxIn、非数组形态)已由
// dynq_test.go 的 TestSlices(AsInt64Slice)覆盖,此处只测各函数自身:合法多元素
// 收敛、逐元素失败透传、非数组形态拒绝。逐元素失败用例锁定 mapSlice 的现状
// 契约——原样透传元素自身的收敛错误,错误信息不带下标(Kind/Index 由调用方
// At 补充)。

func TestAsTimeSlice(t *testing.T) {
	t.Run("合法多元素数组_收敛成功_值逐项相等", func(t *testing.T) {
		v, err := AsTimeSlice([]any{"2026-01-02T03:04:05Z", "2026-06-07T08:09:10+08:00"})
		if err != nil {
			t.Fatalf("valid datetime array: %v", err)
		}
		w1, err1 := time.Parse(time.RFC3339, "2026-01-02T03:04:05Z")
		w2, err2 := time.Parse(time.RFC3339, "2026-06-07T08:09:10+08:00")
		if err1 != nil || err2 != nil {
			t.Fatalf("fixture parse: %v %v", err1, err2)
		}
		if len(v) != 2 || !v[0].Equal(w1) || !v[1].Equal(w2) {
			t.Fatalf("values: %v", v)
		}
	})
	t.Run("首元素非法_收敛失败_透传元素自身错误", func(t *testing.T) {
		_, err := AsTimeSlice([]any{"2026-01-02 03:04:05", "2026-01-03T03:04:05Z"})
		wantReasonExact(t, err, `invalid RFC3339 datetime: "2026-01-02 03:04:05"`)
	})
	t.Run("非数组输入_收敛失败_报 expected array", func(t *testing.T) {
		_, err := AsTimeSlice("2026-01-02T03:04:05Z")
		wantErrContaining(t, err, "expected array, got string")
	})
}

func TestAsDateSlice(t *testing.T) {
	t.Run("合法多元素数组_收敛成功_值逐项相等", func(t *testing.T) {
		v, err := AsDateSlice([]any{"2026-01-02", "2026-06-30"})
		if err != nil {
			t.Fatalf("valid date array: %v", err)
		}
		w1, err1 := time.Parse("2006-01-02", "2026-01-02")
		w2, err2 := time.Parse("2006-01-02", "2026-06-30")
		if err1 != nil || err2 != nil {
			t.Fatalf("fixture parse: %v %v", err1, err2)
		}
		if len(v) != 2 || !v[0].Equal(w1) || !v[1].Equal(w2) {
			t.Fatalf("values: %v", v)
		}
	})
	t.Run("尾元素非法_收敛失败_透传元素自身错误", func(t *testing.T) {
		_, err := AsDateSlice([]any{"2026-01-02", "2026-13-01"})
		wantReasonExact(t, err, `invalid date: "2026-13-01" (expected YYYY-MM-DD)`)
	})
	t.Run("非数组输入_收敛失败_报 expected array", func(t *testing.T) {
		_, err := AsDateSlice(map[string]any{"day": "2026-01-02"})
		wantErrContaining(t, err, "expected array, got object")
	})
}

func TestAsUUIDSlice(t *testing.T) {
	t.Run("合法多元素数组_收敛成功_统一为规范小写形式", func(t *testing.T) {
		v, err := AsUUIDSlice([]any{
			"0F14D0AB-9605-4A62-A9E4-5ED26688389B",
			"0f14d0ab-9605-4a62-a9e4-5ed26688389b",
		})
		if err != nil {
			t.Fatalf("valid uuid array: %v", err)
		}
		const canon = "0f14d0ab-9605-4a62-a9e4-5ed26688389b"
		if len(v) != 2 || v[0] != canon || v[1] != canon {
			t.Fatalf("values: %v", v)
		}
	})
	t.Run("中部元素非法_收敛失败_透传元素自身错误", func(t *testing.T) {
		_, err := AsUUIDSlice([]any{
			"0f14d0ab-9605-4a62-a9e4-5ed26688389b",
			"not-a-uuid",
			"e5b8d866-0dcf-4368-8a60-ff18ad1e4e06",
		})
		wantReasonExact(t, err, `invalid uuid: "not-a-uuid"`)
	})
	t.Run("非数组输入_收敛失败_报 expected array", func(t *testing.T) {
		_, err := AsUUIDSlice(float64(3))
		wantErrContaining(t, err, "expected array, got number")
	})
}

func TestAsEnumSlice(t *testing.T) {
	vals := []string{"pending", "active", "banned"}
	t.Run("合法多元素数组_收敛成功_值逐项相等", func(t *testing.T) {
		v, err := AsEnumSlice(vals, []any{"active", "banned", "pending"})
		if err != nil {
			t.Fatalf("valid enum array: %v", err)
		}
		if len(v) != 3 || v[0] != "active" || v[1] != "banned" || v[2] != "pending" {
			t.Fatalf("values: %v", v)
		}
	})
	t.Run("首元素不在白名单_收敛失败_透传元素自身错误", func(t *testing.T) {
		_, err := AsEnumSlice(vals, []any{"ghost", "active"})
		wantReasonExact(t, err, `unknown enum value: "ghost"`)
	})
	t.Run("非数组输入_收敛失败_报 expected array", func(t *testing.T) {
		_, err := AsEnumSlice(vals, "active")
		wantErrContaining(t, err, "expected array, got string")
	})
}

func TestAsBoolSlice(t *testing.T) {
	t.Run("合法多元素数组_收敛成功_值逐项相等", func(t *testing.T) {
		v, err := AsBoolSlice([]any{true, false, true})
		if err != nil {
			t.Fatalf("valid bool array: %v", err)
		}
		if len(v) != 3 || !v[0] || v[1] || !v[2] {
			t.Fatalf("values: %v", v)
		}
	})
	t.Run("中部元素非法_收敛失败_透传元素自身错误", func(t *testing.T) {
		_, err := AsBoolSlice([]any{true, "false", false})
		wantReasonExact(t, err, "expected boolean, got string")
	})
	t.Run("非数组输入_收敛失败_报 expected array", func(t *testing.T) {
		_, err := AsBoolSlice(map[string]any{"flag": true})
		wantErrContaining(t, err, "expected array, got object")
	})
}

// wantReasonExact 对透传错误的 Reason 做全等断言:错误文案被加任何前缀/后缀
// (如下标标记 `[1] ...`)都会在此失败,完整锁定 mapSlice 原样透传元素自身
// 收敛错误、错误信息不带下标的契约。coerce 错误经 coerceErr 构造为 *Error
// (Reason 即文案),AsError 对非 nil 错误必返回非 nil *Error。
func wantReasonExact(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want reason %q, got nil error", want)
	}
	got := AsError(err).Reason
	if got != want {
		t.Fatalf("reason:\n got %q\nwant %q", got, want)
	}
}
