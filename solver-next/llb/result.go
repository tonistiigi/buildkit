package llb

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/contenthash"
	solver "github.com/moby/buildkit/solver-next"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

func ImmutableRefToResult(ref cache.ImmutableRef) solver.Result {
	return &immutableRefResult{ref}
}

type immutableRefResult struct {
	cache.ImmutableRef
}

func (r *immutableRefResult) ID() string {
	return r.ImmutableRef.ID()
}

func (r *immutableRefResult) Release(ctx context.Context) error {
	return r.ImmutableRef.Release(ctx)
}

func (r *immutableRefResult) Sys() interface{} {
	return r.ImmutableRef
}

func NewContentHashFunc(selector string) solver.ResultBasedCacheFunc {
	return func(ctx context.Context, res solver.Result) (digest.Digest, error) {
		ref, ok := res.(*immutableRefResult)
		if !ok {
			return "", errors.Errorf("invalid reference: %T", res)
		}
		return contenthash.Checksum(ctx, ref, selector)
	}
}
