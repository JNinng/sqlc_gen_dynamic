package main

import "testing"

// 安全标识符_原样发射_不加引号:小写字母/下划线开头的小写标识符是 PG 裸合法名。
func TestSQLIdent_SafeBareName_EmittedAsIs(t *testing.T) {
	for _, name := range []string{"users", "_id", "a1_b2", "created_at", "x"} {
		if got := sqlIdent(name); got != name {
			t.Errorf("sqlIdent(%q) = %q, want unchanged", name, got)
		}
	}
}

// 保留字列名/表名_加双引号:RESERVED 与 TYPE_FUNC_NAME 两类裸用会解析失败。
func TestSQLIdent_ReservedKeyword_Quoted(t *testing.T) {
	for _, name := range []string{"order", "group", "user", "check", "select",
		"limit", "offset", "default", "left", "like", "full", "end", "all"} {
		if got := sqlIdent(name); got != `"`+name+`"` {
			t.Errorf("sqlIdent(%q) = %q, want quoted", name, got)
		}
	}
}

// 非常规标识符_加双引号:大小写敏感名与数字开头名裸用会折叠或解析失败
// (内嵌双引号的翻倍转义由 TestSQLIdent_EmbeddedQuote_Doubled 单独覆盖);
// COL_NAME 关键字(如 char/row/numeric)可裸用作列名,不得误伤加引号。
func TestSQLIdent_UnsafeOrCaseSensitiveName_Quoted(t *testing.T) {
	for _, name := range []string{"Order", "USER", "9lives", "a-b", ""} {
		if got := sqlIdent(name); got != `"`+name+`"` {
			t.Errorf("sqlIdent(%q) = %q, want quoted", name, got)
		}
	}
	for _, name := range []string{"char", "row", "numeric", "values", "varchar"} {
		if got := sqlIdent(name); got != name {
			t.Errorf("sqlIdent(%q) = %q, want unchanged (col_name keyword is bare-legal)", name, got)
		}
	}
}

// 内嵌双引号_按SQL规则翻倍转义:quoted identifier 中的 " 须写成 ""。
func TestSQLIdent_EmbeddedQuote_Doubled(t *testing.T) {
	if got := sqlIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("sqlIdent embedded quote = %q, want %q", got, `"we""ird"`)
	}
}
