package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"runtime"
	"sort"
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver"
	"github.com/moby/buildkit/solver/llbsolver/file"
	"github.com/moby/buildkit/solver/llbsolver/ops/fileoptypes"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

const fileCacheType = "buildkit.exec.v0"

type fileOp struct {
	op        *pb.FileOp
	md        *metadata.Store
	w         worker.Worker
	solver    *FileOpSolver
	numInputs int
}

func NewFileOp(v solver.Vertex, op *pb.Op_File, cm cache.Manager, md *metadata.Store, w worker.Worker) (solver.Op, error) {
	return &fileOp{
		op:        op.File,
		md:        md,
		numInputs: len(v.Inputs()),
		w:         w,
		solver:    NewFileOpSolver(&file.Backend{}, file.NewRefManager(cm)),
	}, nil
}

func (f *fileOp) CacheMap(ctx context.Context, index int) (*solver.CacheMap, bool, error) {
	selectors := map[int]map[llbsolver.Selector]struct{}{}

	digester := digest.Canonical.Digester()

	for _, action := range f.op.Actions {
		var dt []byte
		var err error
		switch a := action.Action.(type) {
		case *pb.FileAction_Mkdir:
			p := *a.Mkdir
			p.Owner = nil
			dt, err = json.Marshal(p)
			if err != nil {
				return nil, false, err
			}
		case *pb.FileAction_Mkfile:
			p := *a.Mkfile
			p.Owner = nil
			dt, err = json.Marshal(p)
			if err != nil {
				return nil, false, err
			}
		case *pb.FileAction_Rm:
			p := *a.Rm
			dt, err = json.Marshal(p)
			if err != nil {
				return nil, false, err
			}
		case *pb.FileAction_Copy:
			p := *a.Copy
			p.Owner = nil
			if action.SecondaryInput != -1 && int(action.SecondaryInput) < f.numInputs {
				p.Src = path.Base(p.Src)
				addSelector(selectors, int(action.SecondaryInput), p.Src, p.AllowWildcard)
			}
			dt, err = json.Marshal(p)
			if err != nil {
				return nil, false, err
			}
		}

		if _, err = digester.Hash().Write(dt); err != nil {
			return nil, false, err
		}
	}

	cm := &solver.CacheMap{
		Digest: digester.Digest(),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
		}, f.numInputs),
	}

	for idx, m := range selectors {
		dgsts := make([][]byte, 0, len(m))
		for k := range m {
			dgsts = append(dgsts, []byte(k.Path))
		}
		sort.Slice(dgsts, func(i, j int) bool {
			return bytes.Compare(dgsts[i], dgsts[j]) > 0
		})
		cm.Deps[idx].Selector = digest.FromBytes(bytes.Join(dgsts, []byte{0}))

		cm.Deps[idx].ComputeDigestFunc = llbsolver.NewContentHashFunc(dedupeSelectors(m))
	}

	return cm, true, nil
}

func (f *fileOp) Exec(ctx context.Context, inputs []solver.Result) ([]solver.Result, error) {

	inpRefs := make([]fileoptypes.Ref, 0, len(inputs))
	for i, inp := range inputs {
		workerRef, ok := inp.Sys().(*worker.WorkerRef)
		if !ok {
			return nil, errors.Errorf("invalid reference for exec %T", inp.Sys())
		}
		inpRefs = append(inpRefs, workerRef.ImmutableRef)
		logrus.Debugf("inp %d : %+v", i, workerRef.ImmutableRef)
	}

	outs, err := f.solver.Solve(ctx, inpRefs, f.op.Actions)
	if err != nil {
		return nil, err
	}

	outResults := make([]solver.Result, 0, len(outs))
	for _, out := range outs {
		outResults = append(outResults, worker.NewWorkerRefResult(out.(cache.ImmutableRef), f.w))
	}

	return outResults, nil
}

func addSelector(m map[int]map[llbsolver.Selector]struct{}, idx int, sel string, wildcard bool) {
	mm, ok := m[idx]
	if !ok {
		mm = map[llbsolver.Selector]struct{}{}
		m[idx] = mm
	}
	if wildcard && containsWildcards(sel) {
		mm[llbsolver.Selector{Path: sel, Wildcard: wildcard}] = struct{}{}
	} else {
		mm[llbsolver.Selector{Path: sel}] = struct{}{}
	}
}

func containsWildcards(name string) bool {
	isWindows := runtime.GOOS == "windows"
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if ch == '\\' && !isWindows {
			i++
		} else if ch == '*' || ch == '?' || ch == '[' {
			return true
		}
	}
	return false
}

func dedupeSelectors(m map[llbsolver.Selector]struct{}) []llbsolver.Selector {
	paths := make([]string, 0, len(m))
	for sel := range m {
		if !sel.Wildcard {
			paths = append(paths, sel.Path)
		}
	}
	paths = dedupePaths(paths)
	selectors := make([]llbsolver.Selector, 0, len(m))

	for _, p := range paths {
		selectors = append(selectors, llbsolver.Selector{Path: p})
	}

	for sel := range m {
		if sel.Wildcard {
			selectors = append(selectors, sel)
		}
	}

	sort.Slice(selectors, func(i, j int) bool {
		return selectors[i].Path < selectors[j].Path
	})

	return selectors
}

func NewFileOpSolver(b fileoptypes.Backend, r fileoptypes.RefManager) *FileOpSolver {
	return &FileOpSolver{
		b:    b,
		r:    r,
		outs: map[int]int{},
		ins:  map[int]input{},
	}
}

type FileOpSolver struct {
	b fileoptypes.Backend
	r fileoptypes.RefManager

	mu   sync.Mutex
	outs map[int]int
	ins  map[int]input
	g    flightcontrol.Group
}

type input struct {
	requiresCommit bool
	mount          fileoptypes.Mount
	ref            fileoptypes.Ref
}

func (s *FileOpSolver) Solve(ctx context.Context, inputs []fileoptypes.Ref, actions []*pb.FileAction) ([]fileoptypes.Ref, error) {
	for i, a := range actions {
		logrus.Debugf("action: %+v", a)
		if int(a.Input) < -1 || int(a.Input) >= len(inputs)+len(actions) {
			return nil, errors.Errorf("invalid input index %d, %d provided", a.Input, len(inputs)+len(actions))
		}
		if int(a.SecondaryInput) < -1 || int(a.SecondaryInput) >= len(inputs)+len(actions) {
			return nil, errors.Errorf("invalid secondary input index %d, %d provided", a.Input, len(inputs))
		}

		inp, ok := s.ins[int(a.Input)]
		if ok {
			inp.requiresCommit = true
		}
		s.ins[int(a.Input)] = inp

		inp, ok = s.ins[int(a.SecondaryInput)]
		if ok {
			inp.requiresCommit = true
		}
		s.ins[int(a.SecondaryInput)] = inp

		if a.Output != -1 {
			if _, ok := s.outs[int(a.Output)]; ok {
				return nil, errors.Errorf("duplicate output %d", a.Output)
			}
			idx := len(inputs) + i
			s.outs[int(a.Output)] = idx
			s.ins[idx] = input{requiresCommit: true}
		}
	}

	if len(s.outs) == 0 {
		return nil, errors.Errorf("no outputs specified")
	}

	for i := 0; i < len(s.outs); i++ {
		if _, ok := s.outs[i]; !ok {
			return nil, errors.Errorf("missing output index %d", i)
		}
	}

	defer func() {
		for _, in := range s.ins {
			if in.ref == nil && in.mount != nil {
				in.mount.Release(context.TODO())
			}
		}
	}()

	outs := make([]fileoptypes.Ref, len(s.outs))

	eg, ctx := errgroup.WithContext(ctx)
	for i, idx := range s.outs {
		func(i, idx int) {
			eg.Go(func() error {
				if err := s.validate(idx, inputs, actions, nil); err != nil {
					return err
				}
				inp, err := s.getInput(ctx, idx, inputs, actions)
				if err != nil {
					return err
				}
				outs[i] = inp.ref
				return nil
			})
		}(i, idx)
	}

	if err := eg.Wait(); err != nil {
		for _, r := range outs {
			if r != nil {
				r.Release(context.TODO())
			}
		}
		return nil, err
	}

	return outs, nil
}

func (s *FileOpSolver) validate(idx int, inputs []fileoptypes.Ref, actions []*pb.FileAction, loaded []int) error {
	for _, check := range loaded {
		if idx == check {
			return errors.Errorf("loop from index %d", idx)
		}
	}
	if idx < len(inputs) {
		return nil
	}
	loaded = append(loaded, idx)
	action := actions[idx-len(inputs)]
	for _, inp := range []int{int(action.Input), int(action.SecondaryInput)} {
		if err := s.validate(inp, inputs, actions, loaded); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileOpSolver) getInput(ctx context.Context, idx int, inputs []fileoptypes.Ref, actions []*pb.FileAction) (input, error) {
	inp, err := s.g.Do(ctx, fmt.Sprintf("inp-%d", idx), func(ctx context.Context) (interface{}, error) {
		s.mu.Lock()
		inp := s.ins[idx]
		s.mu.Unlock()
		if inp.mount != nil || inp.ref != nil {
			return inp, nil
		}

		if idx < len(inputs) {
			inp.ref = inputs[idx]
			s.mu.Lock()
			s.ins[idx] = inp
			s.mu.Unlock()
			return inp, nil
		}

		var inpMount, inpMountSecondary fileoptypes.Mount
		action := actions[idx-len(inputs)]

		loadInput := func(ctx context.Context) func() error {
			return func() error {
				inp, err := s.getInput(ctx, int(action.Input), inputs, actions)
				if err != nil {
					return err
				}
				if inp.ref != nil {
					m, err := s.r.Prepare(ctx, inp.ref, false)
					if err != nil {
						return err
					}
					inpMount = m
					return nil
				}
				inpMount = inp.mount
				return nil
			}
		}

		loadSecondaryInput := func(ctx context.Context) func() error {
			return func() error {
				inp, err := s.getInput(ctx, int(action.SecondaryInput), inputs, actions)
				if err != nil {
					return err
				}
				if inp.ref != nil {
					m, err := s.r.Prepare(ctx, inp.ref, true)
					if err != nil {
						return err
					}
					inpMountSecondary = m
					return nil
				}
				inpMountSecondary = inp.mount
				return nil
			}
		}

		if action.Input != -1 && action.SecondaryInput != -1 {
			eg, ctx := errgroup.WithContext(ctx)
			eg.Go(loadInput(ctx))
			eg.Go(loadSecondaryInput(ctx))
			if err := eg.Wait(); err != nil {
				return nil, err
			}
		} else {
			if action.Input != -1 {
				if err := loadInput(ctx)(); err != nil {
					return nil, err
				}
			}
			if action.SecondaryInput != -1 {
				if err := loadSecondaryInput(ctx)(); err != nil {
					return nil, err
				}
			}
		}

		if inpMount == nil {
			m, err := s.r.Prepare(ctx, nil, false)
			if err != nil {
				return nil, err
			}
			inpMount = m
		}

		switch a := action.Action.(type) {
		case *pb.FileAction_Mkdir:
			if err := s.b.Mkdir(ctx, inpMount, *a.Mkdir); err != nil {
				return nil, err
			}
		case *pb.FileAction_Mkfile:
			if err := s.b.Mkfile(ctx, inpMount, *a.Mkfile); err != nil {
				return nil, err
			}
		case *pb.FileAction_Rm:
			if err := s.b.Rm(ctx, inpMount, *a.Rm); err != nil {
				return nil, err
			}
		case *pb.FileAction_Copy:
			if inpMountSecondary == nil {
				m, err := s.r.Prepare(ctx, nil, true)
				if err != nil {
					return nil, err
				}
				inpMountSecondary = m
			}
			if err := s.b.Copy(ctx, inpMountSecondary, inpMount, *a.Copy); err != nil {
				return nil, err
			}
		default:
			return nil, errors.Errorf("invalid action type %T", action.Action)
		}

		if inp.requiresCommit {
			ref, err := s.r.Commit(ctx, inpMount)
			if err != nil {
				return nil, err
			}
			inp.ref = ref
		} else {
			inp.mount = inpMount
		}
		s.mu.Lock()
		s.ins[idx] = inp
		s.mu.Unlock()
		return inp, nil
	})
	if err != nil {
		return input{}, err
	}
	return inp.(input), err
}
