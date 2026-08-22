package dynq

import "strings"

// EscapeLike 全量转义 ILIKE 模式中的 %、_、\ 为字面字符。
// 模糊三 op 的通配符契约:通配符位置完全由 op 语义决定,value 永远是字面量;
// 前端不存在"传 % 实现模糊"的口子。
func EscapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ContainsPattern 构造任意位置匹配模式:%escaped%。
func ContainsPattern(s string) string { return "%" + EscapeLike(s) + "%" }

// PrefixPattern 构造前缀匹配模式:escaped%。
func PrefixPattern(s string) string { return EscapeLike(s) + "%" }

// SuffixPattern 构造后缀匹配模式:%escaped。
func SuffixPattern(s string) string { return "%" + EscapeLike(s) }
