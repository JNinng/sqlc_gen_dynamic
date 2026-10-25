package main

import "strings"

// ident.go:物理列名/表名 → SQL 文本的安全引用形态。
//
// 裸标识符(^[a-z_][a-z0-9_]*$ 且非 PG 保留字)原样发射;其余(保留字、大小写
// 敏感名、含非常规字符)统一加双引号。引号词表取 PostgreSQL kwlist.h 的
// RESERVED_KEYWORD 与 TYPE_FUNC_NAME_KEYWORD 两类——按 PG 语义,这两类不可
// 裸用作列名/表名;COL_NAME_KEYWORD(如 char/row/numeric)与 UNRESERVED 可裸用,
// 不入表。引号化只发生在生成期,产物中的字符串常量已是安全形态,运行期零成本;
// 多引号无害,词表宁多勿漏。

// sqlIdent 返回物理名在 SQL 文本中的安全形态:裸标识符原样,否则加双引号
// (内嵌双引号按 SQL 规则翻倍转义)。
func sqlIdent(name string) string {
	if bareSQLIdent(name) {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// bareSQLIdent 报告 name 是否可裸用作 SQL 标识符:全小写字母/下划线开头、
// 后续可含数字,且不是 PG 保留字。
func bareSQLIdent(name string) bool {
	if name == "" || pgReservedKeywords[name] {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z' || c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// pgReservedKeywords 是 PostgreSQL kwlist.h 中 RESERVED_KEYWORD 与
// TYPE_FUNC_NAME_KEYWORD 的并集(这两类裸用作列名/表名会解析失败)。
// 取自 PostgreSQL 17;后续版本新增保留字时按需补录(漏词故障是产物 SQL 语法
// 错误,多词只是多余引号,发现新词直接加)。
var pgReservedKeywords = map[string]bool{
	// RESERVED_KEYWORD
	"all": true, "analyse": true, "analyze": true, "and": true, "any": true,
	"array": true, "as": true, "asc": true, "asymmetric": true, "both": true,
	"case": true, "cast": true, "check": true, "collate": true, "column": true,
	"constraint": true, "create": true, "current_catalog": true,
	"current_date": true, "current_role": true, "current_time": true,
	"current_timestamp": true, "current_user": true, "default": true,
	"deferrable": true, "desc": true, "distinct": true, "do": true, "else": true,
	"end": true, "except": true, "false": true, "fetch": true, "for": true,
	"foreign": true, "from": true, "grant": true, "group": true, "having": true,
	"in": true, "initially": true, "intersect": true, "into": true,
	"lateral": true, "leading": true, "limit": true, "localtime": true,
	"localtimestamp": true, "not": true, "null": true, "offset": true, "on": true,
	"only": true, "or": true, "order": true, "placing": true, "primary": true,
	"references": true, "returning": true, "select": true, "some": true,
	"symmetric": true, "system_user": true, "table": true, "then": true,
	"to": true, "trailing": true, "true": true, "union": true, "unique": true,
	"user": true, "using": true, "variadic": true, "when": true, "where": true,
	"window": true, "with": true,
	// TYPE_FUNC_NAME_KEYWORD(reserved,可作函数/类型名,不可作列名/表名)
	"authorization": true, "binary": true, "collation": true,
	"concurrently": true, "cross": true, "current_schema": true, "freeze": true,
	"full": true, "ilike": true, "inner": true, "is": true, "isnull": true,
	"join": true, "left": true, "like": true, "natural": true, "notnull": true,
	"outer": true, "overlaps": true, "right": true, "similar": true,
	"tablesample": true, "verbose": true,
}
