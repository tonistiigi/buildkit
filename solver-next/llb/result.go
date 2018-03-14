package llb

import (
	"bytes"
	"context"
	"path"
	"strings"
	"time"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/contenthash"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func NewWorkerRefResult(ref cache.ImmutableRef, worker worker.Worker) solver.Result {
	return &workerRefResult{&WorkerRef{ImmutableRef: ref, Worker: worker}}
}

type WorkerRef struct {
	ImmutableRef cache.ImmutableRef
	Worker       worker.Worker
}

func (wr *WorkerRef) ID() string {
	return wr.Worker.ID() + "::" + wr.ImmutableRef.ID()
}

type workerRefResult struct {
	*WorkerRef
}

func (r *workerRefResult) Release(ctx context.Context) error {
	return r.ImmutableRef.Release(ctx)
}

func (r *workerRefResult) Sys() interface{} {
	return r.WorkerRef
}

func NewContentHashFunc(selectors []string) solver.ResultBasedCacheFunc {
	return func(ctx context.Context, res solver.Result) (digest.Digest, error) {
		ref, ok := res.Sys().(*WorkerRef)
		if !ok {
			return "", errors.Errorf("invalid reference: %T", res)
		}

		dgsts := make([][]byte, len(selectors))

		eg, ctx := errgroup.WithContext(ctx)

		for i, sel := range selectors {
			// FIXME(tonistiigi): enabling this parallelization seems to create wrong results for some big inputs(like gobuild)
			// func(i int) {
			// 	eg.Go(func() error {
			dgst, err := contenthash.Checksum(ctx, ref.ImmutableRef, path.Join("/", sel))
			if err != nil {
				return "", err
			}
			dgsts[i] = []byte(dgst)
			// return nil
			// })
			// }(i)
		}

		if err := eg.Wait(); err != nil {
			return "", err
		}

		return digest.FromBytes(bytes.Join(dgsts, []byte{0})), nil
	}
}

func newCacheResultStorage(wc *worker.Controller) solver.CacheResultStorage {
	return &cacheResultStorage{
		wc: wc,
	}
}

type cacheResultStorage struct {
	wc *worker.Controller
}

func (s *cacheResultStorage) Save(res solver.Result) (solver.CacheResult, error) {
	ref, ok := res.Sys().(*WorkerRef)
	if !ok {
		return solver.CacheResult{}, errors.Errorf("invalid result: %T", res.Sys())
	}
	if !cache.HasCachePolicyRetain(ref.ImmutableRef) {
		if err := cache.CachePolicyRetain(ref.ImmutableRef); err != nil {
			return solver.CacheResult{}, err
		}
		ref.ImmutableRef.Metadata().Commit()
	}
	return solver.CacheResult{ID: ref.ID(), CreatedAt: time.Now()}, nil
}
func (s *cacheResultStorage) Load(ctx context.Context, res solver.CacheResult) (solver.Result, error) {
	return s.load(res.ID)
}

func (s *cacheResultStorage) load(id string) (solver.Result, error) {
	workerID, refID, err := parseWorkerRef(id)
	if err != nil {
		return nil, err
	}
	w, err := s.wc.Get(workerID)
	if err != nil {
		return nil, err
	}
	ref, err := w.LoadRef(refID)
	if err != nil {
		return nil, err
	}
	return NewWorkerRefResult(ref, w), nil
}

func (s *cacheResultStorage) LoadRemote(ctx context.Context, res solver.CacheResult) (*solver.Remote, error) {
	return nil, nil
}
func (s *cacheResultStorage) Exists(id string) bool {
	ref, err := s.load(id)
	if err != nil {
		return false
	}
	ref.Release(context.TODO())
	return true
}

func parseWorkerRef(id string) (string, string, error) {
	parts := strings.Split(id, "::")
	if len(parts) != 2 {
		return "", "", errors.Errorf("invalid workerref id: %s", id)
	}
	return parts[0], parts[1], nil
}
