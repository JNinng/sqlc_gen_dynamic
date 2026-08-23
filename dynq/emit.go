package dynq

import "strings"

// 本文件是生成代码的辅助函数集合:占位符展开、指针/切片转换。
// 模糊三 op 的 ILIKE 模式构造见 like.go。

// Placeholders 生成 n 个逗号分隔的 "?" 占位符。
func Placeholders(n int) string { return placeholders(n, "") }

// PlaceholdersCast 生成 n 个带显式 cast 后缀的占位符(如 "?::uuid"),用于
// 文本参数与 uuid/enum/jsonb/int_array 列的跨类型比较(显式 cast 不依赖
// 预编译语句的参数类型推断,任何执行模式下语义一致)。
func PlaceholdersCast(n int, cast string) string { return placeholders(n, cast) }

func placeholders(n int, cast string) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('?')
		b.WriteString(cast)
	}
	return b.String()
}

// Anys 将任意切片转为 []any 以展开进 sq.Expr。
func Anys[T any](vals []T) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}

// Addr 返回 v 的指针(用于枚举等需类型转换的 Params 字段赋值)。
func Addr[T any](v T) *T { return &v }

// ConvertSlice 将 []string 转为字符串底层类型的切片(如生成的枚举类型)。
func ConvertSlice[T ~string](vals []string) []T {
	out := make([]T, len(vals))
	for i, v := range vals {
		out[i] = T(v)
	}
	return out
}
