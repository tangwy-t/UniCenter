package datascope

import (
	"context"
)

// SelfDimensionResolver resolves the "self" dimension data scope.
type SelfDimensionResolver struct{}

// NewSelfDimensionResolver creates a SelfDimensionResolver.
func NewSelfDimensionResolver() *SelfDimensionResolver {
	return &SelfDimensionResolver{}
}

// DimensionType returns "self".
func (r *SelfDimensionResolver) DimensionType() string { return "self" }

// Resolve resolves the self dimension scope.
// ScopeSelf returns a slice containing only selfID; other levels return nil (all).
func (r *SelfDimensionResolver) Resolve(ctx context.Context, level int8, selfID uint64, userID uint64) (*ResolvedDimension, error) {
	dim := &ResolvedDimension{Level: level, SelfID: selfID}
	if level == ScopeSelf {
		dim.AllowedIDs = []uint64{selfID}
	}
	// nil = all for non-ScopeSelf levels
	return dim, nil
}
