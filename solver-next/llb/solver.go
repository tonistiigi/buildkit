package llb

import (
	"context"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/worker"
)

type ExporterRequest struct {
	// Exporter       exporter.ExporterInstance
	// ExportCacheRef string
}

// ResolveWorkerFunc returns default worker for the temporary default non-distributed use cases
type ResolveWorkerFunc func() (worker.Worker, error)

// ResolveOpFunc finds an Op implementation for a vertex
type ResolveOpFunc func(solver.Vertex) (solver.Op, error)

type Solver struct {
	solver        *solver.JobList // TODO: solver.Solver
	resolveWorker ResolveWorkerFunc
	frontends     map[string]frontend.Frontend
	// ce            *cacheimport.CacheExporter
	// ci            *cacheimport.CacheImporter
}

func New(resolve ResolveOpFunc, resolveWorker ResolveWorkerFunc, f map[string]frontend.Frontend, cache solver.CacheManager) *Solver {
	s := &Solver{
		resolveWorker: resolveWorker,
		frontends:     f,
	}
	s.solver = solver.NewJobList(solver.SolverOpt{
		ResolveOpFunc: s.resolver(resolve),
		DefaultCache:  cache,
	})
	return s
}

func (s *Solver) resolver(f ResolveOpFunc) solver.ResolveOpFunc {
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

	res, exporterAttrs, err := s.Bridge(j).Solve(ctx, req)
	if err != nil {
		return err
	}

	defer func() {
		if res != nil {
			go res.Release(context.TODO())
		}
	}()

	_ = exporterAttrs

	//
	// if exp := req.Exporter; exp != nil {
	// 	if err := inVertexContext(ctx, exp.Name(), func(ctx context.Context) error {
	// 		return exp.Export(ctx, immutable, exporterOpt)
	// 	}); err != nil {
	// 		return err
	// 	}
	// }
	//
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
