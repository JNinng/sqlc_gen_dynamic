package dynq

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// 类型收敛(严格策略,§6.3):数值只收 JSON number 且整数类型校验 float64 整数性;
// integer 额外收十进制字符串——protojson 对 int64 的无损口径即字符串,JSON number
// 形态在 2^53 之外必然精度已损;datetime 只收 RFC3339;date 只收 YYYY-MM-DD;枚举
// 只收白名单内字符串;数组逐元素收敛。所有函数返回 *Error(Reason 已填,Kind/
// Index/Field 由调用方经 At 补充)。

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

// AsInt64 收敛整数:JSON number(校验 float64 整数性)、Go 原生整数类型,或
// 十进制字符串——where 值只能出自 JSON 反序列化,>2^53 的 int64 主键经 JS number
// 传输必然精度已损(静默错位比 400 更危险),protojson 的无损口径是字符串;
// 字符串形态只收十进制整数字面量(可选符号),精确 ParseInt,与 asCursorInt64
// 同构,其余形态一律拒绝。
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
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, coerceErr("invalid integer: %q", n)
		}
		return i, nil
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

// asCursorInt64 收敛整型游标值:只收 EncodeCursor 产出的十进制字符串并精确
// 解析,与 AsDecimal 同构——游标值只能出自 EncodeCursor,数字形态必然意味着
// 篡改或精度已损(float64 无法无损承载 >2^53 的整型主键),故拒收一切数字
// 形态;对前导 + 的宽容与 isDecimalLiteral 的「可选符号」一致,值语义不受
// 影响。协议门(where 值)不经此函数,走 AsInt64——字符串与数字形态均收。
func asCursorInt64(v any) (int64, error) {
	s, ok := v.(string)
	if !ok {
		return 0, coerceErr("expected integer string, got %s", typeOf(v))
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, coerceErr("invalid integer: %q", s)
	}
	return n, nil
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

// AsDecimal 收敛 decimal(decimal 游标值):只收字符串——EncodeCursor 产出的
// 规范十进制字面量或 NaN/±Infinity 哨兵——原样透传,精度不在 Go 侧解释,
// 由 seek 占位符的 ?::numeric cast 交服务端精确解析。不收 JSON number:
// 游标值只能出自 EncodeCursor,数字形态必然意味着篡改或精度已损(float64
// 无法无损承载 NUMERIC(40,10) 量级的值)。
func AsDecimal(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", coerceErr("expected decimal string, got %s", typeOf(v))
	}
	if !isDecimalLiteral(s) {
		return "", coerceErr("invalid decimal: %q", s)
	}
	return s, nil
}

// isDecimalLiteral 校验编码器产出的字面量形态:可选符号 + 数字与至多一个小数点
// (不收指数形态——编码器只产出十进制展开,指数形态只能来自契约外输入)。
func isDecimalLiteral(s string) bool {
	switch s {
	case sentinelNaN, sentinelPosInf, sentinelNegInf:
		return true
	}
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits, dots := 0, 0
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return digits > 0
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
