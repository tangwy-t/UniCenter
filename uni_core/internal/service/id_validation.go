package service

import (
	"context"
	"fmt"

	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// validateIDsExist checks that every requested ID exists in the repository,
// returning a 400 BadRequest naming the missing IDs otherwise.
//
// fetch must return the subset of ids that actually exist (may be a subset or
// superset of requested — only requested-missing matters). The existence check
// is intentionally best-effort and runs outside the write transaction: if an
// ID is deleted concurrently after validation, the FK constraint on the join
// table still surfaces a loud error rather than a phantom row.
func validateIDsExist(ctx context.Context, kind string, requested []uint64, fetch func(context.Context, []uint64) ([]uint64, error)) error {
	requested = util.DedupIDs(requested)
	if len(requested) == 0 {
		return nil
	}
	existing, err := fetch(ctx, requested)
	if err != nil {
		return err
	}
	missing := util.MissingIDs(requested, existing)
	if len(missing) == 0 {
		return nil
	}
	return apperror.BadRequest(fmt.Sprintf("%s不存在: %s", kind, util.FormatIDs(missing)))
}
