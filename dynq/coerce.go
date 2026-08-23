package dynq

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// 类型收敛(严格策略,§6.3):数值只收 JSON number 且整数类型校验 float64 整数性,
// 不收字符串数字;datetime 只收 RFC3339;date 只收 YYYY-MM-DD;枚举只收白名单内
// 字符串;数组逐元素收敛。所有函数返回 *Error(Reason 已填,Kind/Index/Field 由
// 调用方经 At 补充)。

const nullValueHint = "value is null (use is_null / not_null ops instead)"

func coerceErr(format string, args ...any) *Error {
	return NewErr("", "", fmt.Sprintf(format, args...), -1)
}

func typeOf(v any) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case float64:
		return "number"
	case string:
		return "string"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// AsInt64 收敛整数:JSON number(校验 float64 整数性)或 Go 原生整数类型;
// 不收字符串数字。
func AsInt64(v any) (int64, error) {
	if v == nil {
		return 0, coerceErr(nullValueHint)
	}
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
			return 0, coerceErr("expected integer number, got non-integer %v", n)
		}
		if n < -9.007199254740992e15 || n > 9.007199254740992e15 {
			return 0, coerceErr("integer out of range: %v", n)
		}
		return int64(n), nil
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case int32:
		return int64(n), nil
	default:
		return 0, coerceErr("expected integer number, got %s", typeOf(v))
	}
}

// AsFloat64 收敛数值:JSON number 或 Go 原生数值类型,不收字符串数字。
func AsFloat64(v any) (float64, error) {
	if v == nil {
		return 0, coerceErr(nullValueHint)
	}
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case int32:
		return float64(n), nil
	default:
		return 0, coerceErr("expected number, got %s", typeOf(v))
	}
}

// AsString 收敛字符串。
func AsString(v any) (string, error) {
	if v == nil {
		return "", coerceErr(nullValueHint)
	}
	s, ok := v.(string)
	if !ok {
		return "", coerceErr("expected string, got %s", typeOf(v))
	}
	return s, nil
}

// AsBool 收敛布尔。
func AsBool(v any) (bool, error) {
	if v == nil {
		return false, coerceErr(nullValueHint)
	}
	b, ok := v.(bool)
	if !ok {
		return false, coerceErr("expected boolean, got %s", typeOf(v))
	}
	return b, nil
}

// AsTime 收敛 datetime:只收 RFC3339 字符串。
func AsTime(v any) (time.Time, error) {
	if v == nil {
		return time.Time{}, coerceErr(nullValueHint)
	}
	s, ok := v.(string)
	if !ok {
		return time.Time{}, coerceErr("expected RFC3339 datetime string, got %s", typeOf(v))
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, coerceErr("invalid RFC3339 datetime: %q", s)
	}
	return t, nil
}

// AsDate 收敛 date:只收 YYYY-MM-DD。
func AsDate(v any) (time.Time, error) {
	if v == nil {
		return time.Time{}, coerceErr(nullValueHint)
	}
	s, ok := v.(string)
	if !ok {
		return time.Time{}, coerceErr("expected date string (YYYY-MM-DD), got %s", typeOf(v))
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, coerceErr("invalid date: %q (expected YYYY-MM-DD)", s)
	}
	return t, nil
}

// AsUUID 收敛 uuid:字符串且格式合法,返回规范形式。
func AsUUID(v any) (string, error) {
	if v == nil {
		return "", coerceErr(nullValueHint)
	}
	s, ok := v.(string)
	if !ok {
		return "", coerceErr("expected uuid string, got %s", typeOf(v))
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return "", coerceErr("invalid uuid: %q", s)
	}
	return u.String(), nil
}

// AsEnum 收敛枚举:只收白名单内字符串。
func AsEnum(vals []string, v any) (string, error) {
	if v == nil {
		return "", coerceErr(nullValueHint)
	}
	s, ok := v.(string)
	if !ok {
		return "", coerceErr("expected enum string, got %s", typeOf(v))
	}
	for _, want := range vals {
		if s == want {
			return s, nil
		}
	}
	return "", coerceErr("unknown enum value: %q", s)
}

// AsJSON 收敛 json 值:任意 JSON,序列化为紧凑字节串(jsonb 参数化占位用)。
func AsJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, coerceErr(nullValueHint)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, coerceErr("invalid json value: %v", err)
	}
	return b, nil
}

// AsInt64Slice 收敛 int64 数组:非空、长度 ≤ MaxIn、逐元素收敛。
func AsInt64Slice(v any) ([]int64, error) {
	return mapSlice(v, func(item any) (int64, error) { return AsInt64(item) })
}

// AsFloat64Slice 收敛 float64 数组。
func AsFloat64Slice(v any) ([]float64, error) {
	return mapSlice(v, func(item any) (float64, error) { return AsFloat64(item) })
}

// AsStringSlice 收敛 string 数组。
func AsStringSlice(v any) ([]string, error) {
	return mapSlice(v, func(item any) (string, error) { return AsString(item) })
}

// AsBoolSlice 收敛 bool 数组。
func AsBoolSlice(v any) ([]bool, error) {
	return mapSlice(v, func(item any) (bool, error) { return AsBool(item) })
}

// AsTimeSlice 收敛 datetime 数组。
func AsTimeSlice(v any) ([]time.Time, error) {
	return mapSlice(v, func(item any) (time.Time, error) { return AsTime(item) })
}

// AsDateSlice 收敛 date 数组。
func AsDateSlice(v any) ([]time.Time, error) {
	return mapSlice(v, func(item any) (time.Time, error) { return AsDate(item) })
}

// AsUUIDSlice 收敛 uuid 数组。
func AsUUIDSlice(v any) ([]string, error) {
	return mapSlice(v, func(item any) (string, error) { return AsUUID(item) })
}

// AsEnumSlice 收敛枚举数组:逐元素比对白名单。
func AsEnumSlice(vals []string, v any) ([]string, error) {
	return mapSlice(v, func(item any) (string, error) { return AsEnum(vals, item) })
}

func mapSlice[T any](v any, coerce func(any) (T, error)) ([]T, error) {
	if v == nil {
		return nil, coerceErr(nullValueHint)
	}
	items, ok := v.([]any)
	if !ok {
		return nil, coerceErr("expected array, got %s", typeOf(v))
	}
	if len(items) == 0 {
		return nil, coerceErr("array must not be empty")
	}
	if len(items) > MaxIn {
		return nil, coerceErr("array length %d exceeds MaxIn %d", len(items), MaxIn)
	}
	out := make([]T, 0, len(items))
	for _, item := range items {
		t, err := coerce(item)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
