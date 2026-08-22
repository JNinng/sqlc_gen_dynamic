package dynq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// 协议限额(§6.4 默认值)。
const (
	// MaxIn 是 in/not_in 单数组长度上限。
	MaxIn = 1000
	// MaxWhere 是 where 条数上限。
	MaxWhere = 20
	// PlaceholderBudget 是单语句占位符总预算:所有 in/not_in 数组长度之和 + 条数。
	// 远低于 PG 单语句 65535 协议上限的交叉风险面。
	PlaceholderBudget = 60000
	// MaxOffset 是 offset 模式归一化后 Offset 的上限。
	MaxOffset = 100000
	// MaxBodyBytes 是请求体上限。
	MaxBodyBytes = 1 << 20
)

// Query 是全资源通用的查询协议信封。三个数组与 pagination 均可缺省
// (nil 视为空 / offset 模式首页)。
type Query struct {
	Fields     []string    `json:"fields"`
	Where      []Condition `json:"where"`
	Sort       []SortItem  `json:"sort"`
	Pagination *Pagination `json:"pagination"`
}

// Condition 是单个 where 条件。多条条件之间恒为 AND,不支持 OR 与条件分组。
// JSON 协议面统一小驼峰(接口交互契约)。
type Condition struct {
	Field      string `json:"field"`
	Op         string `json:"op"`
	Value      any    `json:"value"`
	RightValue any    `json:"rightValue"`
}

// SortItem 是单个排序项。
type SortItem struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

// Pagination 是分页输入。page 从 1 开始;after 非空即游标模式;
// includeTotal 默认 true(nil 视为 true)。
type Pagination struct {
	Page         *int    `json:"page"`
	PageSize     *int    `json:"pageSize"`
	After        *string `json:"after"`
	IncludeTotal *bool   `json:"includeTotal"`
}

// IncludeTotalOf 返回 include_total 的有效值:缺省/nil = true。
// 该字段属 handler 层消费,不进入 Params 与查询引擎。
func IncludeTotalOf(p *Pagination) bool {
	if p == nil || p.IncludeTotal == nil {
		return true
	}
	return *p.IncludeTotal
}

// DecodeQuery 严格解码查询信封:请求体上限 1MB、未知 JSON 键拒绝、
// 拒绝尾随数据。
func DecodeQuery(r io.Reader) (*Query, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes+1))
	if err != nil {
		return nil, NewErr(KindRequest, "", "read request body: "+err.Error(), -1)
	}
	if len(body) > MaxBodyBytes {
		return nil, NewErr(KindRequest, "", fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes), -1)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var q Query
	if err := dec.Decode(&q); err != nil {
		return nil, NewErr(KindRequest, "", "malformed request: "+err.Error(), -1)
	}
	if dec.More() {
		return nil, NewErr(KindRequest, "", "trailing data after request body", -1)
	}
	return &q, nil
}

// NormalizeOrder 归一化排序方向:asc/desc 大小写不敏感,其他值拒绝。
// 返回 desc bool(false = ASC)。
func NormalizeOrder(s string) (bool, error) {
	switch {
	case equalFold(s, "asc"):
		return false, nil
	case equalFold(s, "desc"):
		return true, nil
	default:
		return false, NewErr(KindSort, "", "invalid order: "+s+" (expected asc or desc)", -1)
	}
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
