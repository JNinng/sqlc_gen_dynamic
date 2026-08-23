package dynq

import (
	"context"
	"testing"
)

type testScope struct {
	UserID  string
	Scopes  []int64
	DeptIDs []string
}

func TestScopeContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := ScopeFromContext[testScope](ctx); got != nil {
		t.Fatalf("empty ctx: got %v, want nil", got)
	}
	s := &testScope{UserID: "u1", Scopes: []int64{1, 7}}
	ctx = WithScope(ctx, s)
	if got := ScopeFromContext[testScope](ctx); got != s {
		t.Fatalf("round trip: got %v, want %v", got, s)
	}
}

func TestScopeContextWrongType(t *testing.T) {
	type otherScope struct{ UserID string }
	ctx := WithScope(context.Background(), &otherScope{UserID: "u1"})
	if got := ScopeFromContext[testScope](ctx); got != nil {
		t.Fatalf("wrong type: got %v, want nil", got)
	}
}

func TestScopeContextNonPointer(t *testing.T) {
	// WithScope 约定存指针;误存值类型时取出 *T 应为 nil,不 panic。
	ctx := WithScope(context.Background(), testScope{UserID: "u1"})
	if got := ScopeFromContext[testScope](ctx); got != nil {
		t.Fatalf("value type: got %v, want nil", got)
	}
}

func TestErrScopeRequired(t *testing.T) {
	e := ErrScopeRequired()
	if e.Kind != KindScope {
		t.Fatalf("kind = %q, want %q", e.Kind, KindScope)
	}
	if e.Field != "" || e.Index != -1 {
		t.Fatalf("unexpected localization: field=%q index=%d", e.Field, e.Index)
	}
}
