package dynq

import "context"

// WithScope 把数据权限上下文放入 ctx:middleware 解析认证后调用一次,
// 存储类型由各资源的生成 Scope 结构体决定(如 *gen.CustomersScope)。
// handler 经 ScopeFromContext[T] 取出并填入 params.Scope。
func WithScope(ctx context.Context, scope any) context.Context {
	return context.WithValue(ctx, scopeCtxKey{}, scope)
}

// ScopeFromContext 取出类型匹配的数据权限上下文;ctx 未携带或类型不符时
// 返回 nil——@scope 表的 nil 即 fail-closed 报错,不做隐式放行。
// 同一 ctx 只应携带一种 Scope 类型(每请求对应一个资源查询场景);
// 需要并存多种时由调用方自行定义外层结构体。
func ScopeFromContext[T any](ctx context.Context) *T {
	s, _ := ctx.Value(scopeCtxKey{}).(*T)
	return s
}

// scopeCtxKey 是 ctx 携带数据权限上下文的私有键类型。
type scopeCtxKey struct{}
