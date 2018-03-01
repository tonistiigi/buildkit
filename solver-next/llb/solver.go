package llb

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/session"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
)

type ExporterRequest struct {
	Exporter       exporter.ExporterInstance
	ExportCacheRef string
}

// ResolveWorkerFunc returns default worker for the temporary default non-distributed use cases
type ResolveWorkerFunc func() (worker.Worker, error)

type Solver struct {
	solver        *solver.JobList // TODO: solver.Solver
	resolveWorker ResolveWorkerFunc
	frontends     map[string]frontend.Frontend
	// ce            *cacheimport.CacheExporter
	// ci            *cacheimport.CacheImporter
}

func New(wc *worker.Controller, f map[string]frontend.Frontend, cacheStore solver.CacheKeyStorage) *Solver {
	s := &Solver{
		resolveWorker: defaultResolver(wc),
		frontends:     f,
	}

	results := newCacheResultStorage(wc)

	cache := solver.NewCacheManager("local", cacheStore, results)

	s.solver = solver.NewJobList(solver.SolverOpt{
		ResolveOpFunc: s.resolver(),
		DefaultCache:  cache,
	})
	return s
}

func (s *Solver) resolver() solver.ResolveOpFunc {
	return func(v solver.Vertex, b solver.Builder) (solver.Op, error) {
		w, err := s.resolveWorker()
		if err != nil {
			return nil, err
		}
		return w.ResolveOp(v, s.Bridge(b))
	}
}

func (s *Solver) Bridge(b solver.Builder) frontend.FrontendLLBBridge {
	return &llbBridge{
		builder:       b,
		frontends:     s.frontends,
		resolveWorker: s.resolveWorker,
	}
}

func (s *Solver) Solve(ctx context.Context, id string, req frontend.SolveRequest, exp ExporterRequest) error {
	j, err := s.solver.NewJob(id)
	if err != nil {
		return err
	}

	defer j.Discard()

	j.SessionID = session.FromContext(ctx)

	res, exporterOpt, err := s.Bridge(j).Solve(ctx, req)
	if err != nil {
		return err
	}

	defer func() {
		if res != nil {
			go res.Release(context.TODO())
		}
	}()

	if exp := exp.Exporter; exp != nil {
		var immutable cache.ImmutableRef
		if res != nil {
			workerRef, ok := res.Sys().(*WorkerRef)
			if !ok {
				return errors.Errorf("invalid reference: %T", res.Sys())
			}
			immutable = workerRef.ImmutableRef
		}

		// if err := inVertexContext(ctx, exp.Name(), func(ctx context.Context) error {
		//
		// }); err != nil {
		// 	return err
		// }

		return exp.Export(ctx, immutable, exporterOpt)
	}

	// if exportName := req.ExportCacheRef; exportName != "" {
	// 	if err := inVertexContext(ctx, "exporting build cache", func(ctx context.Context) error {
	// 		cache, err := j.cacheExporter(ref)
	// 		if err != nil {
	// 			return err
	// 		}
	//
	// 		records, err := cache.Export(ctx)
	// 		if err != nil {
	// 			return err
	// 		}
	//
	// 		// TODO: multiworker
	// 		return s.ce.Export(ctx, records, exportName)
	// 	}); err != nil {
	// 		return err
	// 	}
	// }

	return err
}

func (s *Solver) Status(ctx context.Context, id string, statusChan chan *client.SolveStatus) error {
	j, err := s.solver.Get(id)
	if err != nil {
		return err
	}
	return j.Status(ctx, statusChan)
}

func defaultResolver(wc *worker.Controller) ResolveWorkerFunc {
	return func() (worker.Worker, error) {
		return wc.GetDefault()
	}
}
