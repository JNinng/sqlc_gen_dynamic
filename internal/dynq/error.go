package dynq

import (
	"errors"
	"fmt"
)

// Error kinds. Kind 为空表示由具体消费方(生成代码)补充定位信息。
const (
	KindWhere      = "where"
	KindSort       = "sort"
	KindFields     = "fields"
	KindPagination = "pagination"
	KindCursor     = "cursor"
	KindScope      = "scope"
	KindOption     = "option"
	KindRequest    = "request"
)

// Error 是 dynq 唯一的校验错误形态:任何冲突、越权、形态错误的输入都显式拒绝,
// 不存在静默覆盖、静默修正、静默 no-op 路径。
type Error struct {
	Kind   string `json:"kind"`
	Index  int    `json:"index,omitempty"` // 数组下标;-1 表示不适用
	Field  string `json:"field,omitempty"`
	Reason string `json:"reason"`
}

func (e *Error) Error() string {
	if e.Field != "" && e.Index >= 0 {
		return fmt.Sprintf("dynq: [%s] %s (field %q, index %d)", e.Kind, e.Reason, e.Field, e.Index)
	}
	if e.Field != "" {
		return fmt.Sprintf("dynq: [%s] %s (field %q)", e.Kind, e.Reason, e.Field)
	}
	return fmt.Sprintf("dynq: [%s] %s", e.Kind, e.Reason)
}

// At 返回补充了定位信息(Kind/Index/Field)的错误副本,Kind 为空时保留原 Kind。
func (e *Error) At(kind string, index int, field string) *Error {
	ne := *e
	if kind != "" {
		ne.Kind = kind
	}
	ne.Index = index
	ne.Field = field
	return &ne
}

// NewErr 构造带定位的错误。index < 0 时不填。
func NewErr(kind, field, reason string, index int) *Error {
	return &Error{Kind: kind, Field: field, Reason: reason, Index: index}
}

// ErrScopeRequired 表示声明了 @scope 的资源未注入数据权限上下文:
// fail-closed——宁可拒绝服务也不静默退化为全量可见。
func ErrScopeRequired() *Error {
	return &Error{Kind: KindScope, Reason: "data scope required: this resource declares @scope (set params.Scope from server-side auth context)", Index: -1}
}

// AsError 将任意 error 收敛为 *Error;非 *Error 的错误视为 request 形态。
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Kind: KindRequest, Reason: err.Error()}
}
