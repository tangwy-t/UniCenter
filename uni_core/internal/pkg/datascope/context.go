package datascope

import "context"

// ScopeContext holds the resolved scope context for a user, stored in context.Context.
type ScopeContext struct {
	UserID     uint64
	Dimensions map[string]*ResolvedDimension // key = dimension type
}

// ResolvedDimension is the result of resolving a single dimension.
type ResolvedDimension struct {
	Level      int8
	SelfID     uint64
	AllowedIDs []uint64 // nil = ScopeAll (all allowed); empty slice = no accessible items
}

type scopeCtxKey struct{}

// WithScopeContext injects a ScopeContext into context.Context.
func WithScopeContext(ctx context.Context, sc *ScopeContext) context.Context {
	return context.WithValue(ctx, scopeCtxKey{}, sc)
}

// ScopeContextFromCtx extracts a ScopeContext from context.Context.
func ScopeContextFromCtx(ctx context.Context) (*ScopeContext, bool) {
	sc, ok := ctx.Value(scopeCtxKey{}).(*ScopeContext)
	return sc, ok
}
