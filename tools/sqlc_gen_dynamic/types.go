package main

import (
	"fmt"
	"strings"

	"github.com/jinzhu/inflection"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// PG 类型 → dataType 映射(§7.1)与能力矩阵(§7.2)。
// null op 与类型矩阵正交:不入预设、不随可空性自动授予,唯一前提是列可空。

// mappedDataType 未知 dataType 表示该列不支持动态过滤/排序(仅可投影)。
type mappedDataType struct {
	dataType string // dynq.DataType 值
	goType   string // Params Go 类型
	// numericCast:PG numeric 列的参数需显式 ?::numeric(float8 → numeric 无隐式比较)。
	numericCast bool
}

func mapPGType(col *pb.Column) (mappedDataType, error) {
	t := col.Type
	name := t.Name
	if col.IsArray {
		if name == "int4" || name == "integer" {
			return mappedDataType{dataType: "int_array", goType: "[]int64"}, nil
		}
		return mappedDataType{}, fmt.Errorf("array type %q is not supported for dynamic columns", name)
	}
	switch name {
	case "int2", "int4", "int8", "smallint", "integer", "bigint",
		"serial", "bigserial", "smallserial", "serial2", "serial4", "serial8":
		return mappedDataType{dataType: "integer", goType: "int64"}, nil
	case "numeric":
		return mappedDataType{dataType: "number", goType: "float64", numericCast: true}, nil
	case "float4", "float8", "real", "double precision":
		return mappedDataType{dataType: "number", goType: "float64"}, nil
	case "timestamptz", "timestamp with time zone":
		return mappedDataType{dataType: "datetime", goType: "time.Time"}, nil
	case "timestamp", "timestamp without time zone":
		return mappedDataType{dataType: "datetime", goType: "time.Time"}, nil
	case "date":
		return mappedDataType{dataType: "date", goType: "time.Time"}, nil
	case "varchar", "character varying", "bpchar", "char", "character", "text":
		return mappedDataType{dataType: "string", goType: "string"}, nil
	case "bool", "boolean":
		return mappedDataType{dataType: "boolean", goType: "bool"}, nil
	case "uuid":
		return mappedDataType{dataType: "uuid", goType: "string"}, nil
	case "jsonb", "json":
		return mappedDataType{dataType: "jsonb", goType: "[]byte"}, nil
	default:
		return mappedDataType{}, fmt.Errorf("type %q is not supported for dynamic columns", displayName(t))
	}
}

func displayName(t *pb.Identifier) string {
	if t.Schema != "" {
		return t.Schema + "." + t.Name
	}
	return t.Name
}

// capabilityMatrix:dataType × 默认可用 op(§7.2)。
var capabilityMatrix = map[string]map[string]bool{
	"integer":  opsSet("eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte", "between"),
	"number":   opsSet("eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte", "between"),
	"datetime": opsSet("eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte", "between"),
	"date":     opsSet("eq", "neq", "in", "not_in", "gt", "gte", "lt", "lte", "between"),
	"string":   opsSet("eq", "neq", "in", "not_in", "contains", "prefix", "suffix"),
	"boolean":  opsSet("eq", "neq", "in", "not_in"),
	"uuid":     opsSet("eq", "neq", "in", "not_in"),
	"enum":     opsSet("eq", "neq", "in", "not_in"),
	"jsonb":    opsSet("has"),
	// int_array 仅 in(&& 数组重叠语义)。
	"int_array": opsSet("in"),
}

func opsSet(ops ...string) map[string]bool {
	m := make(map[string]bool, len(ops))
	for _, op := range ops {
		m[op] = true
	}
	return m
}

// presetOps 预设展开(§5.4)。预设名 fuzzy 与 op contains 显式区分。
var presetOps = map[string][]string{
	"exact": {"eq", "neq"},
	"in":    {"in", "not_in"},
	"range": {"gt", "gte", "lt", "lte", "between"},
	"fuzzy": {"contains", "prefix", "suffix"},
	"has":   {"has"},
}

var knownOps = map[string]bool{
	"eq": true, "neq": true, "gt": true, "gte": true, "lt": true, "lte": true,
	"between": true, "in": true, "not_in": true, "prefix": true, "suffix": true,
	"contains": true, "is_null": true, "not_null": true, "has": true,
}

// expandDynamicItems 展开 @dynamic 的预设与 op 列表:
//   - 预设项与类型能力取交集(§7.2 最终 op 集合 = 类型能力 ∩ DSL 声明),
//     如 int_array 列声明预设 in 时 not_in 被裁剪而非报错;
//   - 显式 op 项严格校验:类型不支持 → 生成期 fail(§5.3.9);
//   - is_null/not_null 不入预设、不从可空性授予,声明于非空列 → fail。
func expandDynamicItems(where, dataType, payload string, nullable bool) ([]string, error) {
	allowed := capabilityMatrix[dataType]
	var out []string
	seen := map[string]bool{}
	addStrict := func(op string) error {
		if !knownOps[op] {
			return fmt.Errorf("%s: unknown @dynamic item %q", where, op)
		}
		if (op == "is_null" || op == "not_null") && !nullable {
			return fmt.Errorf("%s: op %q declared on NOT NULL column", where, op)
		}
		if op != "is_null" && op != "not_null" && !allowed[op] {
			return fmt.Errorf("%s: op %q not supported by dataType %s", where, op, dataType)
		}
		if !seen[op] {
			seen[op] = true
			out = append(out, op)
		}
		return nil
	}
	addIntersect := func(op string) {
		if (op == "is_null" || op == "not_null") && !nullable {
			return
		}
		if op != "is_null" && op != "not_null" && !allowed[op] {
			return
		}
		if !seen[op] {
			seen[op] = true
			out = append(out, op)
		}
	}
	for _, rawItem := range strings.Split(payload, ",") {
		item := strings.TrimSpace(rawItem)
		if item == "" {
			return nil, fmt.Errorf("%s: empty @dynamic item", where)
		}
		if preset, ok := presetOps[item]; ok {
			for _, op := range preset {
				addIntersect(op)
			}
			continue
		}
		if err := addStrict(item); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: @dynamic declares no supported ops for dataType %s", where, dataType)
	}
	return out, nil
}

// ---- 命名:与 sqlc-gen-go 保持一致的 snake_case → Go 名称转换 ----

// commonInitialisms 与 sqlc-gen-go 的首字母缩写表核心子集一致。
var commonInitialisms = map[string]bool{
	"API": true, "ASCII": true, "CPU": true, "CSS": true, "DNS": true, "EOF": true,
	"GUID": true, "HTML": true, "HTTP": true, "HTTPS": true, "ID": true, "IP": true,
	"JSON": true, "LHS": true, "QPS": true, "RAM": true, "RHS": true, "RPC": true,
	"SLA": true, "SMTP": true, "SSH": true, "TLS": true, "TTL": true, "UI": true,
	"UID": true, "UUID": true, "URI": true, "URL": true, "UTF8": true, "VM": true,
	"XML": true, "XSRF": true, "XSS": true,
}

// goName 物理名(snake_case)→ PascalCase,处理常见首字母缩写。
func goName(s string) string {
	var out strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		upper := strings.ToUpper(part)
		if commonInitialisms[upper] {
			out.WriteString(upper)
			continue
		}
		out.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return out.String()
}

// camelKey 物理列名(snake_case)→ 逻辑键(camelCase)。
// 注意:camelCase 不做首字母缩写大写(org_id → orgId,非 orgID)——
// 初始缩写大写仅用于 Go 标识符(goName),逻辑键是对外契约的标准驼峰。
func camelKey(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 {
			b.WriteString(strings.ToLower(part))
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// modelName 表名 → sqlc-gen-go 模型结构体名(单数 + PascalCase)。
func modelName(table string) string { return goName(inflection.Singular(table)) }

// enumGoTypeName PG 枚举类型名 → sqlc-gen-go 枚举 Go 类型名。
func enumGoTypeName(enumName string) string { return goName(enumName) }
