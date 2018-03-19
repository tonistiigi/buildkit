package llb

import (
	"context"
	"io"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/cacheimport"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/frontend"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/util/tracing"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type llbBridge struct {
	builder       solver.Builder
	frontends     map[string]frontend.Frontend
	resolveWorker func() (worker.Worker, error)
	ci            *cacheimport.CacheImporter
	cm            solver.CacheManager
}

func (b *llbBridge) Solve(ctx context.Context, req frontend.SolveRequest) (res solver.CachedResult, exp map[string][]byte, err error) {
	var cm solver.CacheManager
	if req.ImportCacheRef != "" && b.cm == nil {
		var err error
		cm, err = b.ci.Resolve(ctx, req.ImportCacheRef)
		if err != nil {
			return nil, nil, err
		}
	}

	if req.Definition != nil && req.Definition.Def != nil {
		edge, err := Load(req.Definition, WithCacheSource(cm))
		if err != nil {
			return nil, nil, err
		}
		res, err = b.builder.Build(ctx, edge)
		if err != nil {
			return nil, nil, err
		}
	}
	if req.Frontend != "" {
		f, ok := b.frontends[req.Frontend]
		if !ok {
			return nil, nil, errors.Errorf("invalid frontend: %s", req.Frontend)
		}
		res, exp, err = f.Solve(ctx, b, req.FrontendOpt)
		if err != nil {
			return nil, nil, err
		}
	} else {
		if req.Definition == nil || req.Definition.Def == nil {
			return nil, nil, nil
		}
	}

	if res != nil {
		wr, ok := res.Sys().(*worker.WorkerRef)
		if !ok {
			return nil, nil, errors.Errorf("invalid reference for exporting: %T", res.Sys())
		}
		if err := wr.ImmutableRef.Finalize(ctx); err != nil {
			return nil, nil, err
		}
	}
	return
}

func (s *llbBridge) Exec(ctx context.Context, meta executor.Meta, root cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) (err error) {
	w, err := s.resolveWorker()
	if err != nil {
		return err
	}
	span, ctx := tracing.StartSpan(ctx, strings.Join(meta.Args, " "))
	err = w.Exec(ctx, meta, root, stdin, stdout, stderr)
	tracing.FinishWithError(span, err)
	return err
}

func (s *llbBridge) ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error) {
	w, err := s.resolveWorker()
	if err != nil {
		return "", nil, err
	}
	return w.ResolveImageConfig(ctx, ref)
}
