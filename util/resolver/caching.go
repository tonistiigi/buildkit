package resolver

import (
	"context"

	"github.com/containerd/containerd/remotes"
	"github.com/docker/docker/pkg/locker"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

func NewCachingResolver(r remotes.Resolver) remotes.Resolver {
	return NewCache()(r)
}

type cachingResolver struct {
	remotes.Resolver
	locker *locker.Locker
	cache  map[string]cachedResult
}

func (r *cachingResolver) Resolve(ctx context.Context, ref string) (name string, desc specs.Descriptor, err error) {
	r.locker.Lock(ref)
	defer r.locker.Unlock(ref)

	if cr, ok := r.cache[ref]; ok {
		return cr.name, cr.desc, cr.err
	}

	name, desc, err = r.Resolver.Resolve(ctx, ref)
	r.cache[ref] = cachedResult{name: name, desc: desc, err: err}

	return
}

type cachedResult struct {
	name string
	desc specs.Descriptor
	err  error
}

func NewCache() Cache {
	l := locker.New()
	cache := map[string]cachedResult{}
	return func(r remotes.Resolver) remotes.Resolver {
		return &cachingResolver{Resolver: r, locker: l, cache: cache}
	}
}

type Cache func(remotes.Resolver) remotes.Resolver
