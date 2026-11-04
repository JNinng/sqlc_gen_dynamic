package dynq

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// number 的 JSON 哨兵:JSON 无法原生表示 NaN/Infinity(Go json.Marshal 对 NaN
// 直接报错),编码为同名字符串哨兵,解码时还原并仍走类型收敛。
const (
	sentinelNaN    = "NaN"
	sentinelPosInf = "Infinity"
	sentinelNegInf = "-Infinity"
)

// fnv64a 是 FNV-1a 64 位稳定哈希。
func fnv64a(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// cursorPayload 是游标 JSON 载荷:f 为排序指纹的十进制字符串,v 为按有效排序
// 顺序排列的列值。精确值一律字符串编码(指纹、整型、decimal),不依赖解码端
// 配置,任何语言/解码器读载荷均不丢精度;裸 JSON 数字仅 float 列使用——Go 的
// shortest-roundtrip 浮点编码保证其往返无损。
type cursorPayload struct {
	F string `json:"f"`
	V []any  `json:"v"`
}

// EncodeCursor 编码不透明游标:base64url(JSON{f, v})。
// 列值按有效排序的 dataType 格式化:datetime 为 RFC3339、date 为 YYYY-MM-DD、
// 整数为十进制字符串(>2^53 的整型经 JSON number 落 float64 即丢精度,雪花等
// 大整型主键将不可翻页)、number 的 NaN/±Inf 为字符串哨兵、decimal 为十进制
// 字符串(NaN/±Inf 同哨兵)、其余原生 JSON;NULL(指针 nil / pgtype Valid=false)
// 为 JSON null。
// 游标不签名:仅定位"调用方本就可发起的同一查询"的读取位置,不构成权限边界。
func EncodeCursor(fp uint64, seq []SortField, vals []any) (string, error) {
	if len(vals) != len(seq) {
		return "", NewErr(KindCursor, "", "cursor value count mismatch", -1)
	}
	encoded := make([]any, len(vals))
	for i, v := range vals {
		ev, err := encodeCursorValue(seq[i].DataType, v)
		if err != nil {
			return "", err
		}
		encoded[i] = ev
	}
	payload, err := json.Marshal(cursorPayload{
		F: strconv.FormatUint(fp, 10),
		V: encoded,
	})
	if err != nil {
		return "", NewErr(KindCursor, "", "encode cursor: "+err.Error(), -1)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeCursor 解码并校验游标:base64url/JSON 解码、比对排序指纹、按 dataType
// 收敛列值类型,不符 → 错误(Kind = cursor)。协议门与 typed 门共用此校验路径。
func DecodeCursor(cursor string, fp uint64, seq []SortField) ([]any, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, NewErr(KindCursor, "", "cursor is not valid base64url", -1)
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, NewErr(KindCursor, "", "cursor is not valid JSON", -1)
	}
	if payload.F != strconv.FormatUint(fp, 10) {
		return nil, NewErr(KindCursor, "", "fingerprint mismatch (sort order changed or cursor from another query/resource)", -1)
	}
	if len(payload.V) != len(seq) {
		return nil, NewErr(KindCursor, "", fmt.Sprintf("cursor has %d values, want %d", len(payload.V), len(seq)), -1)
	}
	vals := make([]any, len(seq))
	for i, v := range payload.V {
		cv, err := decodeCursorValue(seq[i].DataType, v)
		if err != nil {
			return nil, AsError(err).At(KindCursor, -1, string(seq[i].Key))
		}
		vals[i] = cv
	}
	return vals, nil
}

// encodeCursorValue 将模型行字段值按 dataType 编码为可 JSON 化的值。
// 覆盖 sqlc-gen-go(pgx/v5)模型的字段类型:原生类型、指针,以及 pgtype
// 包装类型(Timestamptz/Date/UUID/Numeric,Valid=false 编码为 null)。
func encodeCursorValue(dt DataType, v any) (any, error) {
	switch v := v.(type) {
	case nil:
		return nil, nil
	case time.Time:
		return formatCursorTime(dt, v)
	case *time.Time:
		if v == nil {
			return nil, nil
		}
		return formatCursorTime(dt, *v)
	case pgtype.Timestamptz:
		if !v.Valid {
			return nil, nil
		}
		return formatCursorTime(dt, v.Time)
	case pgtype.Date:
		if !v.Valid {
			return nil, nil
		}
		return formatCursorTime(dt, v.Time)
	case pgtype.Timestamp:
		if !v.Valid {
			return nil, nil
		}
		return formatCursorTime(dt, v.Time)
	case pgtype.UUID:
		if !v.Valid {
			return nil, nil
		}
		return uuid.UUID(v.Bytes).String(), nil
	case pgtype.Numeric:
		return formatCursorNumeric(v), nil
	case *pgtype.Numeric:
		if v == nil {
			return nil, nil
		}
		return formatCursorNumeric(*v), nil
	case string:
		return v, nil
	case *string:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case bool:
		return v, nil
	case *bool:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case *int64:
		if v == nil {
			return nil, nil
		}
		return strconv.FormatInt(*v, 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case *int32:
		if v == nil {
			return nil, nil
		}
		return strconv.FormatInt(int64(*v), 10), nil
	case int16: // smallint 的 sqlc 模型字段
		return strconv.FormatInt(int64(v), 10), nil
	case *int16:
		if v == nil {
			return nil, nil
		}
		return strconv.FormatInt(int64(*v), 10), nil
	case int:
		return strconv.FormatInt(int64(v), 10), nil
	case float64:
		return formatCursorNumber(v), nil
	case *float64:
		if v == nil {
			return nil, nil
		}
		return formatCursorNumber(*v), nil
	case float32: // real 的 sqlc 模型字段;float32→float64 精确,seek 按 float8 比较
		return formatCursorNumber(float64(v)), nil
	case *float32:
		if v == nil {
			return nil, nil
		}
		return formatCursorNumber(float64(*v)), nil
	case uuid.UUID:
		return v.String(), nil
	case *uuid.UUID:
		if v == nil {
			return nil, nil
		}
		return v.String(), nil
	case json.RawMessage:
		return v, nil
	case []byte:
		if v == nil {
			return nil, nil
		}
		return json.RawMessage(v), nil
	default:
		return nil, NewErr(KindCursor, "", fmt.Sprintf("cannot encode cursor value of type %T", v), -1)
	}
}

func formatCursorTime(dt DataType, t time.Time) (any, error) {
	if dt == DataTypeDate {
		return t.Format("2006-01-02"), nil
	}
	return t.Format(time.RFC3339Nano), nil
}

func formatCursorNumber(f float64) any {
	switch {
	case math.IsNaN(f):
		return sentinelNaN
	case math.IsInf(f, 1):
		return sentinelPosInf
	case math.IsInf(f, -1):
		return sentinelNegInf
	default:
		return f
	}
}

// formatCursorNumeric 将 numeric 列值编码为游标值:NULL 与 NaN/±Infinity 同
// number 哨兵约定,有限值编码为保留标度的十进制字符串(Int × 10^Exp)。
// 字符串化不损失精度,排序语义不变:游标值仅作 seek 参数,比较在服务端以
// ?::numeric 精确进行;1.5 与 1.50 编码串不同但 numeric 等值判定等价,
// 不影响续翻定位。
func formatCursorNumeric(n pgtype.Numeric) any {
	switch {
	case !n.Valid:
		return nil
	case n.NaN:
		return sentinelNaN
	case n.InfinityModifier == pgtype.Infinity:
		return sentinelPosInf
	case n.InfinityModifier == pgtype.NegativeInfinity:
		return sentinelNegInf
	default:
		return numericDecimalString(n.Int, n.Exp)
	}
}

// numericDecimalString 将 pgtype.Numeric 的有限值渲染为十进制字符串,文本形态
// 与 pgx 编码器一致(Int == nil 是零值 Numeric 的形态,渲染为 "0";
// 二进制扫描的零值为 Int=0、Exp=0,走通用路径结果相同)。
func numericDecimalString(intVal *big.Int, exp int32) string {
	if intVal == nil {
		return "0"
	}
	s := intVal.String() // 含符号
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch e := int(exp); {
	case e >= 0:
		b.WriteString(s)
		b.WriteString(strings.Repeat("0", e))
	default:
		if k := -e; len(s) <= k {
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", k-len(s)))
			b.WriteString(s)
		} else {
			i := len(s) - k
			b.WriteString(s[:i])
			b.WriteByte('.')
			b.WriteString(s[i:])
		}
	}
	return b.String()
}

// decodeCursorValue 将游标 JSON 值按 dataType 收敛为可参数化的 Go 值。
// NULL(JSON null)是合法游标值,保留为 nil,由 seek 谓词的 NULL 分支处理。
func decodeCursorValue(dt DataType, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch dt {
	case DataTypeInteger:
		return asCursorInt64(v)
	case DataTypeNumber:
		return decodeCursorNumber(v)
	case DataTypeDecimal:
		return AsDecimal(v)
	case DataTypeDatetime:
		return AsTime(v)
	case DataTypeDate:
		return AsDate(v)
	case DataTypeUUID:
		return AsUUID(v)
	case DataTypeBoolean:
		return AsBool(v)
	case DataTypeJSONB:
		return AsJSON(v)
	default: // string / enum
		return AsString(v)
	}
}

func decodeCursorNumber(v any) (any, error) {
	if s, ok := v.(string); ok {
		switch s {
		case sentinelNaN:
			return math.NaN(), nil
		case sentinelPosInf:
			return math.Inf(1), nil
		case sentinelNegInf:
			return math.Inf(-1), nil
		}
	}
	return AsFloat64(v)
}
