package llb

import (
	"context"
	"io"
	"strings"

	"github.com/moby/buildkit/cache"
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
}

func (b *llbBridge) Solve(ctx context.Context, req frontend.SolveRequest) (res solver.Result, exp map[string][]byte, err error) {

	if req.Definition != nil {
		edge, err := Load(req.Definition) // TODO: append cache
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
		immutable, ok := res.Sys().(cache.ImmutableRef)
		if !ok {
			return nil, nil, errors.Errorf("invalid reference for exporting: %T", res.Sys())
		}
		if err := immutable.Finalize(ctx); err != nil {
			return nil, nil, err
		}
		return
	} else {
		return nil, nil, errors.Errorf("invalid build request")
	}
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
