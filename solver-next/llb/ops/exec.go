package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/solver-next/llb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/logs"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const execCacheType = "buildkit.exec.v0"

type execOp struct {
	op        *pb.ExecOp
	cm        cache.Manager
	exec      executor.Executor
	numInputs int
}

func NewExecOp(v solver.Vertex, op *pb.Op_Exec, cm cache.Manager, exec executor.Executor) (solver.Op, error) {
	return &execOp{
		op:        op.Exec,
		cm:        cm,
		exec:      exec,
		numInputs: len(v.Inputs()),
	}, nil
}

func cloneExecOp(old *pb.ExecOp) pb.ExecOp {
	n := *old
	for i := range n.Mounts {
		*n.Mounts[i] = *n.Mounts[i]
	}
	return n
}

func (e *execOp) CacheMap(ctx context.Context) (*solver.CacheMap, error) {

	op := cloneExecOp(e.op)
	for i := range op.Mounts {
		op.Mounts[i].Selector = ""
	}

	dt, err := json.Marshal(struct {
		Type string
		Exec *pb.ExecOp
		OS   string
		Arch string
	}{
		Type: execCacheType,
		Exec: &op,
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	})
	if err != nil {
		return nil, err
	}

	cm := &solver.CacheMap{
		Digest: digest.FromBytes(dt),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
		}, e.numInputs),
	}

	deps, err := e.getMountDeps()
	if err != nil {
		return nil, err
	}

	for i, dep := range deps {
		if dep.Selector != "" {
			cm.Deps[i].Selector = digest.FromBytes([]byte(dep.Selector))
		}
		if dep.ContentBasedHash {
			cm.Deps[i].ComputeDigestFunc = llb.NewContentHashFunc(dep.Selector)
		}
	}

	return cm, nil
}

type dep struct {
	Selector         string
	ContentBasedHash bool
}

func (e *execOp) getMountDeps() ([]dep, error) {
	deps := make([]dep, e.numInputs)
	for _, m := range e.op.Mounts {
		if m.Input == pb.Empty {
			continue
		}
		if int(m.Input) >= len(deps) {
			return nil, errors.Errorf("invalid mountinput %v", m)
		}

		if m.Selector != "" {
			deps[m.Input].Selector = path.Join("/", m.Selector)
		}

		if m.Readonly && m.Dest != pb.RootMount { // exclude read-only rootfs
			deps[m.Input].ContentBasedHash = true
		}
	}
	return deps, nil
}

type output struct {
	immutable cache.ImmutableRef
	mutable   cache.MutableRef
}

func (e *execOp) Exec(ctx context.Context, inputs []solver.Result) ([]solver.Result, error) {
	var mounts []executor.Mount
	var root cache.Mountable

	var outputs []cache.Ref

	defer func() {
		for _, o := range outputs {
			if o != nil {
				go o.Release(context.TODO())
			}
		}
	}()

	for _, m := range e.op.Mounts {
		var mountable cache.Mountable
		var ref cache.ImmutableRef
		if m.Input != pb.Empty {
			if int(m.Input) > len(inputs) {
				return nil, errors.Errorf("missing input %d", m.Input)
			}
			inp := inputs[int(m.Input)]
			var ok bool
			ref, ok = inp.Sys().(cache.ImmutableRef)
			if !ok {
				return nil, errors.Errorf("invalid reference for exec %T", inputs[int(m.Input)])
			}
			mountable = ref
		}
		if m.Output != pb.SkipOutput {
			if m.Readonly && ref != nil && m.Dest != pb.RootMount { // exclude read-only rootfs
				out, err := e.cm.Get(ctx, ref.ID()) // TODO: add dup to immutableRef
				if err != nil {
					return nil, err
				}
				outputs = append(outputs, out)
			} else {
				active, err := e.cm.New(ctx, ref, cache.WithDescription(fmt.Sprintf("mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " ")))) // TODO: should be method
				if err != nil {
					return nil, err
				}
				outputs = append(outputs, active)
				mountable = active
			}
		}
		if m.Dest == pb.RootMount {
			root = mountable
		} else {
			mounts = append(mounts, executor.Mount{Src: mountable, Dest: m.Dest, Readonly: m.Readonly, Selector: m.Selector})
		}
	}

	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Dest < mounts[j].Dest
	})

	meta := executor.Meta{
		Args: e.op.Meta.Args,
		Env:  e.op.Meta.Env,
		Cwd:  e.op.Meta.Cwd,
		User: e.op.Meta.User,
	}

	stdout, stderr := logs.NewLogStreams(ctx)
	defer stdout.Close()
	defer stderr.Close()

	if err := e.exec.Exec(ctx, meta, root, mounts, nil, stdout, stderr); err != nil {
		return nil, errors.Wrapf(err, "executor failed running %v", meta.Args)
	}

	refs := []solver.Result{}
	for i, out := range outputs {
		if mutable, ok := out.(cache.MutableRef); ok {
			ref, err := mutable.Commit(ctx)
			if err != nil {
				return nil, errors.Wrapf(err, "error committing %s", mutable.ID())
			}
			refs = append(refs, llb.ImmutableRefToResult(ref))
		} else {
			refs = append(refs, llb.ImmutableRefToResult(out.(cache.ImmutableRef)))
		}
		outputs[i] = nil
	}
	return refs, nil
}
