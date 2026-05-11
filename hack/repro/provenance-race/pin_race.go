package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/pkg/errors"
)

func envDuration(key string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}

// runPinRace reproduces issue #6731 ("provenance: image source pin can be read
// empty during capture") via the SAME mechanism as the #5606 panic but
// surfaced through the production guard's blind spot: the guard skips when
// st.op.op == nil, but a SourceOp with state.op.op != nil and pin == "" is
// not nil and bypasses the guard. captureProvenance then calls SourceOp.Pin()
// during the post-instance-pre-CacheKey window of SourceOp.CacheMap, gets
// (non-nil id, ""), and ImageIdentifier.Capture's digest.Parse("") returns
// "invalid checksum digest format" — surfacing as the client-visible
// "failed to capture provenance: failed to parse image digest : invalid
// checksum digest format" error.
//
// Three solves in one c.Build, sharing actives, with a slow source pin:
//
//  1. warmup (no IgnoreCache, original chain): synchronous Evaluate.
//     Populates state[base_D], state[mid_M], state[root_R] — all with
//     resolved ops and complete root edge. state[mid_M].vtx.Inputs()
//     points at base_D.
//
//  2. race-creator (IgnoreCache on base, FRESH mid/root digest): async
//     Evaluate. Load shifts base to D' (creating state[base_D']) AND
//     creates a fresh state[mid_M2] whose vtx.Inputs() points at D' and
//     a fresh state[root_R2]. Scheduling state[root_R2] walks down and
//     calls state[base_D'].getEdge — populating state[base_D'].op as a
//     sharedOp and triggering CacheMap. SourceOp.CacheMap calls
//     instance() (sets s.id) then sleeps (BUILDKIT_REPRO_DELAY_SOURCE_PIN)
//     before src.CacheKey writes s.pin. During this window state[base_D']
//     looks resolved (op != nil, op.op != nil) but the inner SourceOp
//     has empty pin.
//
//  3. walker (IgnoreCache on base, ORIGINAL mid/root digest): synchronous
//     Evaluate, run after a short head-start so the race-creator is
//     guaranteed to be in the sleep window. Load takes the line 558 reuse
//     path on actives[base_D'] (already there from step 2), and reuses
//     state[mid_M] and state[root_R] from step 1. scheduler.build on the
//     root edge sees the complete edge from step 1 and short-circuits —
//     never schedules state[base_D'] itself for this solve. Then
//     captureProvenance walks via the walker's wrapper graph, reaches
//     state[base_D'], finds op.op != nil so the production guard passes,
//     calls f(SourceOp), reads pin == "", and the digest.Parse error
//     bubbles up to the client.
//
// The race surfaces WITHOUT the local walkProvenance error-return
// instrumentation in solver/jobs.go — only the SourceOp pin-window sleep is
// needed, and that is purely timing (no logic change).
func runPinRace(ctx context.Context, gw gateway.Client, opt runOpt) (*gateway.Result, error) {
	base := llb.Image(opt.image)
	baseIC := llb.Image(opt.image, llb.IgnoreCache)

	// mkChain builds a graph using the given base. When fresh is true, the
	// mid vertex's Op bytes get a unique copy destination, which makes its
	// LLB digest differ from the original chain's mid — that mismatch is
	// what causes the race-creator's load to make a FRESH state[mid_M2]
	// whose vtx.Inputs() references the shifted D'. Without that freshness
	// the race-creator would also reuse state[mid_M] and never schedule
	// state[base_D'].getEdge.
	mkChain := func(b llb.State, fresh bool) llb.State {
		dest := "/x"
		if fresh {
			dest = fmt.Sprintf("/x-fresh-%d", opt.graphIndex)
		}
		intermediate := llb.Scratch().File(
			llb.Copy(b, "/bin/busybox", dest),
			llb.WithCustomNamef("[repro %d/1] pin-race mid fresh=%v", opt.graphIndex, fresh),
		)
		return llb.Merge(
			[]llb.State{b, intermediate},
			llb.WithCustomNamef("[repro %d/1] pin-race root fresh=%v", opt.graphIndex, fresh),
		)
	}

	plain := mkChain(base, false)        // state[base_D], state[mid_M], state[root_R]
	racer := mkChain(baseIC, true)       // state[base_D'], state[mid_M2], state[root_R2]
	walker := mkChain(baseIC, false)     // reuses state[mid_M] and state[root_R]; picks up state[base_D'] via line 558

	// Step 1: warmup. Fully evaluate the plain chain so its root edge is
	// Complete. This is what enables the walker's scheduler to short-circuit
	// later: the walker reuses state[root_R] whose edge is already done.
	plainDef, err := plain.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal plain")
	}
	plainRes, err := gw.Solve(ctx, gateway.SolveRequest{Definition: plainDef.ToPB()})
	if err != nil {
		return nil, errors.Wrap(err, "solve plain")
	}
	plainRef, err := plainRes.SingleRef()
	if err != nil {
		return nil, err
	}
	if err := plainRef.Evaluate(ctx); err != nil {
		return nil, errors.Wrap(err, "evaluate plain (warmup)")
	}

	// Step 2: race-creator. Submit and start its Evaluate in the background.
	// The load runs synchronously inside gw.Solve (the daemon-side resolve
	// of the def), but the actual scheduler.build only starts when Evaluate
	// is called. Once the scheduler reaches state[base_D'].getEdge and
	// triggers SourceOp.CacheMap, the BUILDKIT_REPRO_DELAY_SOURCE_PIN sleep
	// holds it open.
	racerDef, err := racer.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "marshal racer")
	}
	racerRes, err := gw.Solve(ctx, gateway.SolveRequest{Definition: racerDef.ToPB()})
	if err != nil {
		return nil, errors.Wrap(err, "solve racer")
	}
	racerRef, err := racerRes.SingleRef()
	if err != nil {
		return nil, err
	}
	racerDone := make(chan error, 1)
	go func() { racerDone <- racerRef.Evaluate(ctx) }()

	// Step 3 head-start. Wait long enough for the race-creator's scheduler
	// to: load (creates state[base_D']), getEdge state[base_D'] (op set),
	// reach SourceOp.CacheMap, return from instance() (s.id set), and enter
	// the BUILDKIT_REPRO_DELAY_SOURCE_PIN sleep. 500ms is generous for an
	// already-resolved image (warmup pulled it).
	headStart := 500 * time.Millisecond
	if envHeadStart := envDuration("BUILDKIT_REPRO_PIN_RACE_HEADSTART"); envHeadStart > 0 {
		headStart = envHeadStart
	}
	select {
	case err := <-racerDone:
		// Race-creator finished before we even started the walker. Either
		// BUILDKIT_REPRO_DELAY_SOURCE_PIN is unset/too short, or the racer
		// errored. Surface that so the user knows the window wasn't held.
		if err != nil {
			return nil, errors.Wrap(err, "race-creator finished early with error")
		}
		return nil, errors.New("race-creator finished before walker could start: " +
			"set BUILDKIT_REPRO_DELAY_SOURCE_PIN to a duration longer than the warmup overhead (e.g. 5s)")
	case <-time.After(headStart):
	}

	// Step 3: walker. Synchronous Evaluate. Expected to fail with the
	// "failed to parse image digest : invalid checksum digest format"
	// error from captureProvenance.
	walkerDef, err := walker.Marshal(ctx)
	if err != nil {
		// Make sure we don't leave the racer hanging.
		<-racerDone
		return nil, errors.Wrap(err, "marshal walker")
	}
	walkerRes, err := gw.Solve(ctx, gateway.SolveRequest{Definition: walkerDef.ToPB()})
	if err != nil {
		<-racerDone
		return nil, errors.Wrap(err, "solve walker")
	}
	walkerRef, err := walkerRes.SingleRef()
	if err != nil {
		<-racerDone
		return nil, err
	}

	walkErr := walkerRef.Evaluate(ctx)
	// Drain race-creator regardless of walker outcome.
	<-racerDone

	if walkErr != nil {
		// Surface the error. In the success-path of this repro, walkErr is
		// the digest.Parse failure wrapped by captureProvenance.
		return nil, errors.Wrap(walkErr, "walker (expected: digest.Parse \"\" -> invalid checksum digest format)")
	}
	// If walkErr is nil, the race didn't fire on this iteration (timing
	// missed). Return the walker's result so the iteration is reported as
	// successful and the harness moves on.
	return walkerRes, nil
}
