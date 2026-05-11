package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	bkclient "github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	_ "github.com/moby/buildkit/client/connhelper/kubepod"
	_ "github.com/moby/buildkit/client/connhelper/nerdctlcontainer"
	_ "github.com/moby/buildkit/client/connhelper/podmancontainer"
	_ "github.com/moby/buildkit/client/connhelper/ssh"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func main() {
	var (
		addr        string
		image       string
		parallel    int
		iterations  int
		containers  int
		provenance  string
		mode        string
		outputBase  string
		showSuccess bool
		indexOffset int
	)
	flag.StringVar(&addr, "addr", os.Getenv("BUILDKIT_HOST"), "buildkit address, defaults to BUILDKIT_HOST")
	flag.StringVar(&image, "image", "busybox:latest", "image source used by the repro graph")
	flag.IntVar(&parallel, "parallel", 16, "parallel builds")
	flag.IntVar(&iterations, "iterations", 200, "total build iterations")
	flag.IntVar(&containers, "containers", 20, "manual containers started per fanout build")
	flag.StringVar(&provenance, "provenance", "mode=max", "attest:provenance value")
	flag.StringVar(&mode, "mode", "dalec-mergeatpath", "repro mode: gateway-dalec-mergeatpath, dalec-mergeatpath, double-copylink-exec-delayroot, double-copylink-exec, double-merge-exec, double-merge-file, merge-extra-hosts, same-cache-source, fanout, or copylink")
	flag.StringVar(&outputBase, "output-base", "/tmp/buildkit-provenance-race", "directory for local exporter outputs")
	flag.BoolVar(&showSuccess, "success", false, "print successful iterations")
	flag.IntVar(&indexOffset, "index-offset", 0, "value added to iteration index for graph variants")
	flag.Parse()

	if addr == "" {
		addr = "unix:///run/buildkit/buildkitd.sock"
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(errors.WithStack(context.Canceled))

	c, err := bkclient.New(ctx, addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %+v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	var counter atomic.Int64
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(parallel)
	start := time.Now()
	for i := range iterations {
		eg.Go(func() error {
			n := counter.Add(1)
			err := runOne(egCtx, c, runOpt{
				index:      i,
				graphIndex: i + indexOffset,
				image:      image,
				containers: containers,
				provenance: provenance,
				mode:       mode,
				outputBase: outputBase,
			})
			if err != nil {
				return errors.Wrapf(err, "iteration %d", i)
			}
			if showSuccess {
				fmt.Printf("ok iteration=%d completed=%d elapsed=%s\n", i, n, time.Since(start).Round(time.Millisecond))
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "repro failed after %s: %+v\n", time.Since(start).Round(time.Millisecond), err)
		os.Exit(1)
	}
	fmt.Printf("completed %d %s iterations in %s\n", iterations, mode, time.Since(start).Round(time.Millisecond))
}

type runOpt struct {
	index      int
	graphIndex int
	image      string
	containers int
	provenance string
	mode       string
	outputBase string
}

func runOne(ctx context.Context, c *bkclient.Client, opt runOpt) error {
	outputDir := filepath.Join(opt.outputBase, fmt.Sprintf("%s-%d", opt.mode, opt.index))
	if err := os.RemoveAll(outputDir); err != nil {
		return errors.Wrap(err, "remove output dir")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return errors.Wrap(err, "create output dir")
	}
	solveOpt := bkclient.SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": opt.provenance,
		},
		Exports: []bkclient.ExportEntry{{
			Type:      bkclient.ExporterLocal,
			OutputDir: outputDir,
		}},
	}
	switch opt.mode {
	case "multi-ref-overlap", "multi-ref-fanout", "stutter-evaluate":
		// multi-ref returns Refs map; the local exporter+attest provenance combo expects
		// platforms metadata. Drop both for these modes — captureProvenance still runs per
		// ref via resultProxy.Result regardless of frontend attrs.
		solveOpt.FrontendAttrs = nil
		solveOpt.Exports = nil
	}
	_, err := c.Build(ctx, solveOpt, "buildkit_provenance_repro", func(ctx context.Context, gw gateway.Client) (*gateway.Result, error) {
		switch opt.mode {
		case "fanout":
			return runFanout(ctx, gw, opt)
		case "same-cache-source":
			return runSameCacheSource(ctx, gw, opt)
		case "merge-extra-hosts":
			return runMergeExtraHosts(ctx, gw, opt)
		case "dalec-mergeatpath":
			return runDalecMergeAtPath(ctx, gw, opt)
		case "gateway-dalec-mergeatpath":
			return runGatewayDalecMergeAtPath(ctx, gw, opt)
		case "double-copylink-exec", "double-copylink-exec-delayroot":
			return runDoubleCopyLinkExec(ctx, gw, opt)
		case "double-merge-exec":
			return runDoubleMergeExec(ctx, gw, opt)
		case "double-merge-file":
			return runDoubleMergeFile(ctx, gw, opt)
		case "copylink":
			return runCopyLink(ctx, gw, opt)
		case "multi-ref-overlap":
			return runMultiRefOverlap(ctx, gw, opt)
		case "multi-ref-fanout":
			return runMultiRefFanout(ctx, gw, opt)
		case "stutter-evaluate":
			return runStutterEvaluate(ctx, gw, opt)
		case "deep-tostate-chain":
			return runDeepToStateChain(ctx, gw, opt)
		case "ignorecache-shift":
			return runIgnoreCacheShift(ctx, gw, opt)
		case "pin-race":
			return runPinRace(ctx, gw, opt)
		default:
			return nil, errors.Errorf("unknown mode %q", opt.mode)
		}
	}, nil)
	return err
}

func runFanout(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	def, err := llb.Image(opt.image).Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal image state")
	}
	res, err := gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "solve image state")
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(opt.containers)
	for range opt.containers {
		eg.Go(func() error {
			ctr, err := gw.NewContainer(egCtx, gateway.NewContainerRequest{
				Mounts: []gateway.Mount{{
					Dest:      "/",
					MountType: pb.MountType_BIND,
					Ref:       ref,
				}},
			})
			if err != nil {
				return err
			}
			defer ctr.Release(context.TODO())

			p, err := ctr.Start(egCtx, gateway.StartRequest{
				Args: []string{"true"},
			})
			if err != nil {
				return err
			}
			return p.Wait()
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return res, nil
}

type imageRecordType bkclient.UsageRecordType

func (t imageRecordType) SetImageOption(ii *llb.ImageInfo) {
	ii.RecordType = string(t)
}

func runSameCacheSource(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	states := sameCacheImageStates(opt.image)
	solved := make([]*gateway.Result, len(states))
	eg, egCtx := errgroup.WithContext(ctx)
	for i, st := range states {
		eg.Go(func() error {
			def, err := st.Marshal(egCtx)
			if err != nil {
				return errors.Wrap(err, "marshal source state")
			}
			res, err := gw.Solve(egCtx, gateway.SolveRequest{
				Definition: def.ToPB(),
			})
			if err != nil {
				return errors.Wrap(err, "solve source state")
			}
			solved[i] = res
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	eg, egCtx = errgroup.WithContext(ctx)
	eg.SetLimit(opt.containers)
	for i := range opt.containers {
		ref, err := solved[i%len(solved)].SingleRef()
		if err != nil {
			return nil, err
		}
		eg.Go(func() error {
			ctr, err := gw.NewContainer(egCtx, gateway.NewContainerRequest{
				Mounts: []gateway.Mount{{
					Dest:      "/",
					MountType: pb.MountType_BIND,
					Ref:       ref,
				}},
			})
			if err != nil {
				return err
			}
			defer ctr.Release(context.TODO())

			p, err := ctr.Start(egCtx, gateway.StartRequest{
				Args: []string{"true"},
			})
			if err != nil {
				return err
			}
			return p.Wait()
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	srcs := make([]llb.State, 0, len(states))
	for i, st := range states {
		srcs = append(srcs, llb.Scratch().File(llb.Copy(st, "/bin/busybox", fmt.Sprintf("/busybox-%d", i))))
	}
	def, err := llb.Merge(srcs).Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal merged source states")
	}
	return gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
}

func sameCacheImageStates(image string) []llb.State {
	recordTypes := [][]llb.ImageOption{
		nil,
		{llb.MarkImageInternal},
		{imageRecordType(bkclient.UsageRecordTypeFrontend)},
	}
	resolveModes := []llb.ImageOption{
		llb.ResolveModeDefault,
		llb.ResolveModePreferLocal,
		llb.ResolveModeForcePull,
	}
	states := make([]llb.State, 0, len(recordTypes)*len(resolveModes))
	for _, recordType := range recordTypes {
		for _, resolveMode := range resolveModes {
			opts := append([]llb.ImageOption{}, recordType...)
			opts = append(opts, resolveMode)
			states = append(states, llb.Image(image, opts...))
		}
	}
	return states
}

func runMergeExtraHosts(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	left := base.Run(
		llb.Shlex("true"),
		llb.AddExtraHost("merge-cache.example", net.IPv4(127, 0, 0, 1)),
	).Root()
	right := base.Run(
		llb.Shlex("true"),
		llb.AddExtraHost("merge-cache.example", net.IPv4(127, 0, 0, 2)),
	).Root()

	out := llb.Merge([]llb.State{
		llb.Scratch().File(llb.Copy(left, "/bin/busybox", "/left")),
		llb.Scratch().File(llb.Copy(right, "/bin/busybox", "/right")),
	})
	def, err := out.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal merge-extra-hosts state")
	}
	return gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
}

func runDoubleMergeExec(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	lower := make([]llb.State, 4)
	for i := range lower {
		lower[i] = base.Run(
			llb.Shlex("true"),
			llb.AddExtraHost("merge-lower.example", net.IPv4(127, 0, 0, byte(i+1))),
			llb.WithCustomNamef("[repro %d/1] RUN true lower", i),
		).Root()
	}

	upper := make([]llb.State, len(lower))
	for i, st := range lower {
		upper[i] = st.Run(
			llb.Shlex("true"),
			llb.AddExtraHost("merge-upper.example", net.IPv4(127, 0, 1, byte(i+1))),
			llb.WithCustomNamef("[repro %d/1] RUN true upper", i),
		).Root()
	}

	out := llb.Merge([]llb.State{
		llb.Scratch().File(llb.Copy(upper[0], "/bin/busybox", "/out-0")),
		llb.Scratch().File(llb.Copy(upper[1], "/bin/busybox", "/out-1")),
		llb.Scratch().File(llb.Copy(upper[2], "/bin/busybox", "/out-2")),
		llb.Scratch().File(llb.Copy(upper[3], "/bin/busybox", "/out-3")),
	})
	def, err := out.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal double-merge-exec state")
	}
	return gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
}

func runDalecMergeAtPath(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	srcs := make([]llb.State, 4)
	for i := range srcs {
		srcs[i] = base.Run(
			llb.Args([]string{"sh", "-c", "sleep 1"}),
			llb.AddExtraHost("dalec-source.example", net.IPv4(127, 0, 3, byte(i+1))),
			llb.WithCustomNamef("[repro %d/1] RUN sleep dalec source", i),
		).Root()
	}

	lowerDiffs := []llb.State{base}
	for i, st := range srcs {
		copied := llb.Scratch().File(
			llb.Copy(st, "/", "/lower", &llb.CopyInfo{
				CopyDirContentsOnly: true,
				CreateDestPath:      true,
			}),
			llb.WithCustomNamef("[repro %d/1] dalec lower copy", i),
		)
		lowerDiffs = append(lowerDiffs, llb.Diff(base, copied, llb.WithCustomNamef("[repro %d/1] dalec lower diff", i)))
	}
	lower := llb.Merge(lowerDiffs, llb.WithCustomName("[repro] dalec lower merge"))

	upperDiffs := []llb.State{lower}
	for i := range srcs {
		copied := llb.Scratch().File(
			llb.Copy(lower, "/lower", fmt.Sprintf("/upper-%d", i), &llb.CopyInfo{
				CopyDirContentsOnly: true,
				CreateDestPath:      true,
			}),
			llb.WithCustomNamef("[repro %d/1] dalec upper copy", i),
		)
		upperDiffs = append(upperDiffs, llb.Diff(lower, copied, llb.WithCustomNamef("[repro %d/1] dalec upper diff", i)))
	}
	out := llb.Merge(upperDiffs, llb.WithCustomName("[repro] dalec upper merge"))

	def, err := out.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal dalec-mergeatpath state")
	}
	return gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
}

func runGatewayDalecMergeAtPath(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	srcs := make([]llb.State, 4)
	for i := range srcs {
		srcs[i] = base.Run(
			llb.Args([]string{"sh", "-c", "sleep 1"}),
			llb.AddExtraHost("gateway-dalec-source.example", net.IPv4(127, 0, 4, byte(opt.graphIndex+i+1))),
			llb.WithCustomNamef("[repro %d/1] RUN sleep gateway dalec source", i),
		).Root()
	}

	lower := dalecMergeAtPath(base, srcs, "/lower", "gateway dalec lower")
	lowerDef, err := lower.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal gateway dalec lower state")
	}
	lowerRes, err := gw.Solve(ctx, gateway.SolveRequest{
		Definition: lowerDef.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "solve gateway dalec lower state")
	}
	lowerRef, err := lowerRes.SingleRef()
	if err != nil {
		return nil, errors.Wrap(err, "get gateway dalec lower ref")
	}
	forwardedLower, err := lowerRef.ToState()
	if err != nil {
		return nil, errors.Wrap(err, "convert gateway dalec lower ref to state")
	}

	out := dalecMergeAtPath(forwardedLower, []llb.State{forwardedLower}, "/upper", "gateway dalec upper")
	def, err := out.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal gateway dalec upper state")
	}
	var finalRes *gateway.Result
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		res, err := gw.Solve(egCtx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return err
		}
		finalRes = res
		return nil
	})
	eg.Go(func() error {
		return lowerRef.Evaluate(egCtx)
	})
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return finalRes, nil
}

func dalecMergeAtPath(input llb.State, srcs []llb.State, dest, name string) llb.State {
	diffs := []llb.State{input}
	for i, st := range srcs {
		copied := llb.Scratch().File(
			llb.Copy(st, "/", dest, &llb.CopyInfo{
				CopyDirContentsOnly: true,
				CreateDestPath:      true,
			}),
			llb.WithCustomNamef("[repro %d/1] %s copy", i, name),
		)
		diffs = append(diffs, llb.Diff(input, copied, llb.WithCustomNamef("[repro %d/1] %s diff", i, name)))
	}
	return llb.Merge(diffs, llb.WithCustomNamef("[repro] %s merge", name))
}

func runDoubleCopyLinkExec(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	lowerSrc := make([]llb.State, 4)
	for i := range lowerSrc {
		lowerSrc[i] = base.Run(
			llb.Args([]string{"sh", "-c", "sleep 1"}),
			llb.AddExtraHost("copylink-lower.example", net.IPv4(127, 0, 2, byte(i+1))),
			llb.WithCustomNamef("[repro %d/1] RUN sleep lower source", i),
		).Root()
	}

	lowerLinked := make([]llb.State, len(lowerSrc))
	for i, st := range lowerSrc {
		copyLayer := llb.Scratch().File(
			llb.Copy(st, "/bin/busybox", "/linked", &llb.CopyInfo{
				FollowSymlinks:      true,
				CopyDirContentsOnly: true,
				CreateDestPath:      true,
				AllowWildcard:       true,
				AllowEmptyWildcard:  true,
			}),
			llb.WithCustomNamef("[repro %d/1] COPY --link --from=lower /bin/busybox /linked", i),
		)
		lowerLinked[i] = llb.Merge(
			[]llb.State{base, copyLayer},
			llb.WithCustomNamef("[repro %d/1] LINK COPY --link --from=lower", i),
		)
	}

	upperLinked := make([]llb.State, len(lowerLinked))
	for i, st := range lowerLinked {
		copyLayer := llb.Scratch().File(
			llb.Copy(st, "/linked", "/copied", &llb.CopyInfo{
				FollowSymlinks:      true,
				CopyDirContentsOnly: true,
				CreateDestPath:      true,
				AllowWildcard:       true,
				AllowEmptyWildcard:  true,
			}),
			llb.WithCustomNamef("[repro %d/1] COPY --link --from=linked /linked /copied", i),
		)
		upperLinked[i] = llb.Merge(
			[]llb.State{st, copyLayer},
			llb.WithCustomNamef("[repro %d/1] LINK COPY --link --from=linked", i),
		)
	}

	if opt.graphIndex%2 == 1 {
		// Keep dependency vertices identical across adjacent builds, but make the
		// final merge different so this build schedules shared deps while the
		// even-index build can complete from its warmed final cache.
		upperLinked[3] = upperLinked[3].File(llb.Mkfile("/variant", 0o644, []byte("x")))
	}

	mergeOpts := []llb.ConstraintsOpt{}
	if opt.mode == "double-copylink-exec-delayroot" {
		mergeOpts = append(mergeOpts, llb.WithCustomName("[repro] delayroot double-copylink merge"))
	}
	out := llb.Merge([]llb.State{
		llb.Scratch().File(llb.Copy(upperLinked[0], "/copied", "/out-0")),
		llb.Scratch().File(llb.Copy(upperLinked[1], "/copied", "/out-1")),
		llb.Scratch().File(llb.Copy(upperLinked[2], "/copied", "/out-2")),
		llb.Scratch().File(llb.Copy(upperLinked[3], "/copied", "/out-3")),
	}, mergeOpts...)
	def, err := out.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal double-copylink-exec state")
	}
	return gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
}

func runDoubleMergeFile(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	lower := make([]llb.State, 4)
	for i := range lower {
		lower[i] = llb.Scratch().File(
			llb.Copy(base, "/bin/busybox", "/linked"),
			llb.WithCustomNamef("[repro %d/1] LINK COPY --link --from=base", i),
		)
	}

	upper := make([]llb.State, len(lower))
	for i, st := range lower {
		upper[i] = llb.Scratch().File(
			llb.Copy(st, "/linked", "/copied"),
			llb.WithCustomNamef("[repro %d/1] COPY --link --from=linked", i),
		)
	}

	out := llb.Merge([]llb.State{
		llb.Scratch().File(llb.Copy(upper[0], "/copied", "/out-0")),
		llb.Scratch().File(llb.Copy(upper[1], "/copied", "/out-1")),
		llb.Scratch().File(llb.Copy(upper[2], "/copied", "/out-2")),
		llb.Scratch().File(llb.Copy(upper[3], "/copied", "/out-3")),
	})
	def, err := out.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal double-merge-file state")
	}
	return gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
}

// runMultiRefOverlap returns a multi-ref result where each ref shares deep transitive inputs
// with the others. Daemon's solver.go evaluates them in parallel via EachRef, so each ref's
// loadResult/Build/captureProvenance race against each other on shared actives.
func runMultiRefOverlap(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	// 8 source vertices that are each ExecOps with different IPs but same cache key (IPs are
	// cleared in CacheMap). Different LLB digest, same cache key -> scheduler merges these.
	srcs := make([]llb.State, 8)
	for i := range srcs {
		srcs[i] = base.Run(
			llb.Args([]string{"sh", "-c", "true"}),
			llb.AddExtraHost("multi-ref.example", net.IPv4(127, 0, 5, byte(i+1))),
			llb.WithCustomNamef("[repro %d/1] RUN multi-ref source %d", opt.graphIndex, i),
		).Root()
	}
	// 4 different roots, each merging a different combination of sources.
	roots := make([]llb.State, 4)
	for i := range roots {
		diffs := []llb.State{base}
		for j := 0; j < 4; j++ {
			s := srcs[(i+j)%len(srcs)]
			copied := llb.Scratch().File(
				llb.Copy(s, "/", fmt.Sprintf("/m-%d-%d", i, j), &llb.CopyInfo{
					CopyDirContentsOnly: true,
					CreateDestPath:      true,
				}),
				llb.WithCustomNamef("[repro %d/1] multi-ref copy %d/%d", opt.graphIndex, i, j),
			)
			diffs = append(diffs, llb.Diff(base, copied, llb.WithCustomNamef("[repro %d/1] multi-ref diff %d/%d", opt.graphIndex, i, j)))
		}
		roots[i] = llb.Merge(diffs, llb.WithCustomNamef("[repro %d/1] multi-ref merge %d", opt.graphIndex, i))
	}

	res := gateway.NewResult()
	for i, st := range roots {
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "marshal multi-ref root state")
		}
		r, err := gw.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrap(err, "solve multi-ref root")
		}
		ref, err := r.SingleRef()
		if err != nil {
			return nil, err
		}
		res.AddRef(fmt.Sprintf("root-%d", i), ref)
	}
	return res, nil
}

// runMultiRefFanout returns a result with many small refs sharing a common image source.
// The shared source state can have many concurrent CacheMap goroutines via shared sharedOp.
func runMultiRefFanout(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	res := gateway.NewResult()
	const fan = 16
	for i := 0; i < fan; i++ {
		st := base.File(
			llb.Mkfile(fmt.Sprintf("/marker-%d", i), 0o644, []byte("x")),
			llb.WithCustomNamef("[repro %d/1] multi-ref fanout mkfile %d", opt.graphIndex, i),
		)
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "marshal multi-ref fanout root state")
		}
		r, err := gw.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrap(err, "solve multi-ref fanout root")
		}
		ref, err := r.SingleRef()
		if err != nil {
			return nil, err
		}
		res.AddRef(fmt.Sprintf("ref-%d", i), ref)
	}
	return res, nil
}

// runStutterEvaluate triggers a build that does many gw.Solve calls and evaluates them via
// Evaluate concurrently inside the frontend. Each gw.Solve registers a resultProxy in the
// bridge, and concurrent Evaluate calls trigger overlapping load+schedule+walkProvenance.
func runStutterEvaluate(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	srcs := make([]llb.State, 4)
	for i := range srcs {
		srcs[i] = base.Run(
			llb.Args([]string{"sh", "-c", "true"}),
			llb.AddExtraHost("stutter.example", net.IPv4(127, 0, 6, byte(opt.graphIndex+i+1))),
			llb.WithCustomNamef("[repro %d/1] stutter source %d", opt.graphIndex, i),
		).Root()
	}
	stages := make([]gateway.Reference, 0, len(srcs))
	for i, st := range srcs {
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "marshal stutter stage state")
		}
		r, err := gw.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrap(err, "solve stutter stage")
		}
		ref, err := r.SingleRef()
		if err != nil {
			return nil, err
		}
		stages = append(stages, ref)
		_ = i
	}
	// Concurrently evaluate every intermediate ref. Each Evaluate triggers loadResult which
	// loads its def into actives and schedules. They share many vertices (base image and
	// sources via cache-key match) so overlapping load/schedule/walkProvenance happens.
	eg, egCtx := errgroup.WithContext(ctx)
	for _, ref := range stages {
		eg.Go(func() error {
			return ref.Evaluate(egCtx)
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	// Return a single ref so the frontend result is single-ref. The interesting work — many
	// concurrent Evaluate calls overlapping inside actives — already happened above.
	res := gateway.NewResult()
	res.SetRef(stages[len(stages)-1])
	return res, nil
}

// runDeepToStateChain mimics Dalec's pattern of nested gw.Solve + ref.ToState forwarding.
// At each level, srcs are merged via Diff+Merge, then the resulting ref is converted ToState
// and used as the input for the next level. Each level's lower ref is Evaluated concurrently
// with the next level's gw.Solve, maximizing overlap between active scheduling and provenance
// capture across many shared vertices.
func runDeepToStateChain(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	current := base
	const levels = 4
	const sourcesPerLevel = 4
	pendingEvals := []gateway.Reference{}
	for level := 0; level < levels; level++ {
		srcs := make([]llb.State, sourcesPerLevel)
		for i := range srcs {
			srcs[i] = base.Run(
				llb.Args([]string{"sh", "-c", "true"}),
				llb.AddExtraHost("deep-chain.example", net.IPv4(127, byte(level+10), byte(opt.graphIndex), byte(i+1))),
				llb.WithCustomNamef("[repro %d/1] deep src lvl=%d i=%d", opt.graphIndex, level, i),
			).Root()
		}
		stage := dalecMergeAtPath(current, srcs, fmt.Sprintf("/lvl-%d", level), fmt.Sprintf("deep lvl=%d", level))
		def, err := stage.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "marshal deep level %d", level)
		}
		res, err := gw.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, errors.Wrapf(err, "solve deep level %d", level)
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}
		pendingEvals = append(pendingEvals, ref)
		st, err := ref.ToState()
		if err != nil {
			return nil, errors.Wrapf(err, "ToState deep level %d", level)
		}
		current = st
	}

	finalDef, err := current.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal deep final")
	}
	var finalRes *gateway.Result
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		res, err := gw.Solve(egCtx, gateway.SolveRequest{
			Definition: finalDef.ToPB(),
		})
		if err != nil {
			return err
		}
		finalRes = res
		return nil
	})
	for _, ref := range pendingEvals {
		eg.Go(func() error {
			return ref.Evaluate(egCtx)
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return finalRes, nil
}

// runIgnoreCacheShift triggers the dgstWithoutCache shift in Solver.loadUnlocked
// by issuing two concurrent gw.Solve calls that share the SAME root vertex and
// SAME intermediate vertex but the second one marks the deepest vertex with
// IgnoreCache. When the first build's scheduling completes the root edge, the
// second build's scheduler.build returns the existing complete edge without
// running createInputRequests. The second build's wrapper for the deep vertex
// is then at dgstWithoutCache (a state never reached by either scheduler), and
// walkProvenance walking via the second build's wrapper hits a nil-op state.
func runIgnoreCacheShift(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	// Build the same chain in two variants: the deep child differs only in
	// IgnoreCache. Same digest, but different VertexOptions.
	mkChain := func(deepIgnore bool) llb.State {
		runOpts := []llb.RunOption{
			llb.Args([]string{"sh", "-c", "true"}),
			llb.WithCustomNamef("[repro %d/1] ignorecache-shift deep", opt.graphIndex),
		}
		if deepIgnore {
			runOpts = append(runOpts, llb.IgnoreCache)
		}
		deep := base.Run(runOpts...).Root()
		// Intermediate and root are SAME LLB digest in both variants.
		intermediate := llb.Scratch().File(
			llb.Copy(deep, "/", "/x"),
			llb.WithCustomNamef("[repro %d/1] ignorecache-shift mid", opt.graphIndex),
		)
		return llb.Merge([]llb.State{base, intermediate},
			llb.WithCustomNamef("[repro %d/1] ignorecache-shift root", opt.graphIndex))
	}
	noIgnore := mkChain(false)
	withIgnore := mkChain(true)

	noIgnoreDef, err := noIgnore.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal noIgnore")
	}
	withIgnoreDef, err := withIgnore.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal withIgnore")
	}

	// First, fully evaluate the noIgnore variant so its root edge becomes Complete.
	res1, err := gw.Solve(ctx, gateway.SolveRequest{
		Definition: noIgnoreDef.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "solve noIgnore")
	}
	ref1, err := res1.SingleRef()
	if err != nil {
		return nil, err
	}
	if err := ref1.Evaluate(ctx); err != nil {
		return nil, errors.Wrap(err, "evaluate noIgnore")
	}

	// Now solve the withIgnore variant. Its load will SHIFT the deep vertex to
	// dgstWithoutCache (because the noIgnore-variant deep state already exists
	// at the original digest with IgnoreCache=false). The withIgnore root has
	// the SAME digest as noIgnore root (root LLB doesn't depend on deep's
	// IgnoreCache metadata), so its scheduler.build sees a complete edge and
	// short-circuits — never calling createInputRequests on the wrapped chain.
	res2, err := gw.Solve(ctx, gateway.SolveRequest{
		Definition: withIgnoreDef.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrap(err, "solve withIgnore")
	}
	if err := res2.Ref.Evaluate(ctx); err != nil {
		return nil, errors.Wrap(err, "evaluate withIgnore")
	}
	return res2, nil
}

func runCopyLink(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	src := llb.Image(opt.image).File(llb.Mkfile("/fanout-marker", 0o644, []byte("x")))
	left := llb.Scratch().File(llb.Copy(src, "/fanout-marker", "/marker"))
	mid := llb.Scratch().File(llb.Copy(src, "/fanout-marker", "/marker"))
	right := llb.Scratch().File(llb.Copy(src, "/fanout-marker", "/marker"))
	out := llb.Merge([]llb.State{left, mid, right})
	def, err := out.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal copylink-like state")
	}
	return gw.Solve(ctx, gateway.SolveRequest{
		Definition: def.ToPB(),
	})
}
