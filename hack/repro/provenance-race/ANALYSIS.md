# Provenance walkProvenance unresolved-op race: analysis

This reproduces the `nil pointer dereference` panic at
[solver/jobs.go:737](../../../solver/jobs.go#L737) (pre-`983a32b48d`) where
`Job.walkProvenance` reads `st.op.op` while `st.op == nil`. PR #5606
(`983a32b48d` "solver: protect against nil dereference on uninitialized
vertex") added the `if st.op != nil && st.op.op != nil` guard at
[solver/jobs.go:854](../../../solver/jobs.go#L854) in response to a
production panic with this stack trace, but the panic-triggering code path
was not identified at the time.

The mode that reproduces it deterministically is `ignorecache-shift` in
[main.go](main.go). A single `c.Build` call is enough — no concurrency
required. On a clean pre-guard daemon (`4ea0679ee`), the panic fires on the
first iteration.

## The orphan state

The only place that writes `solver.actives` is
[solver/jobs.go:602](../../../solver/jobs.go#L602). The only place that
sets `state.op` is [solver/jobs.go:215](../../../solver/jobs.go#L215),
inside `state.getEdge`. So `actives[X]` can hold a `state{op: nil}` only
when:

1. A `state` was created at key `X` by `loadUnlocked`, and
2. Nobody has ever called `state[X].getEdge`.

Under normal load → schedule flow, every newly created state has its
`getEdge` called: the scheduler walks down from the root via
`createInputRequests`, which calls `Solver.getEdge` on each input, which
calls `state[X].getEdge`. The only way a freshly created state can avoid
that walk is if its parent edge is **already Complete from a prior build**,
so `unpark` short-circuits and `createInputRequests` is skipped — but the
prior build's load must have produced a parent state whose `vtx.Inputs()`
**doesn't reference X**, otherwise that prior build would have scheduled
X itself.

That divergence — same parent state, different child digest in different
loads — is exactly what the `dgstWithoutCache` "shift" produces.

## The shift

[solver/jobs.go:553-582](../../../solver/jobs.go#L553-L582):

```go
dgst := v.Digest()
dgstWithoutCache := digest.FromBytes(fmt.Appendf(nil, "%s-ignorecache", dgst))

// (A) reuse path
st, ok := jl.actives[dgstWithoutCache]
if ok { v = st.vtx }

if !ok {
    st, ok = jl.actives[dgst]
    // (B) shift path: existing !IgnoreCache state, new IgnoreCache load
    if ok && !st.vtx.Options().IgnoreCache && v.Options().IgnoreCache {
        dgst = dgstWithoutCache
    }
    v = &vertexWithCacheOptions{Vertex: v, dgst: dgst, inputs: inputs}
    st, ok = jl.actives[dgst]
}
```

There are two interesting branches:

- **(B) shift path** — an existing `actives[D]` was loaded with
  `IgnoreCache=false` and a later load wants `IgnoreCache=true` for the
  same LLB digest. Digest is rewritten to `D' = digest("D-ignorecache")`,
  a **fresh state** is created at `actives[D']`.
- **(A) reuse path** — `actives[D']` already exists (from an earlier
  shift). Any subsequent load of the same vertex — *regardless of whether
  this load asks for IgnoreCache* — picks up `state[D'].vtx` and returns
  it to its caller.

Both paths produce wrappers whose digest is `D'`. The shift path creates
the orphan; the reuse path lets a build that **never used IgnoreCache**
inherit it.

## Why the orphan stays unscheduled

When the parent state at digest `M` was created by an *earlier* load whose
deep child was `D` (no shift), `state[M].vtx.Inputs()[0].Vertex.Digest() ==
D`. That field is set once at `state` creation
([solver/jobs.go:593](../../../solver/jobs.go#L593)) and never reassigned
— there is no write to `state.vtx` anywhere else in the codebase.

A *new* load that produces a wrapper with `inputs[0].Vertex.Digest() ==
D'` does **not** update `state[M].vtx`. So the two views of "what is M's
input" diverge:

| Source | mid's `Inputs()[0].Vertex.Digest()` |
|---|---|
| `state[M].vtx` (set by the prior load) | `D` |
| the new build's wrapper graph | `D'` |

The scheduler walks via `e.edge.Vertex.Inputs()`
([solver/edge.go:820](../../../solver/edge.go#L820)) where
`e.edge.Vertex` is the state's `vtx`. So scheduling reaches `state[D]`,
not `state[D']`. The complete root edge from the prior build short-circuits
the new build's scheduler, and even if it didn't, the input walk would go
to `D`, not `D'`. **Nobody schedules `state[D']`**, so `state[D'].op`
stays nil.

`walkProvenance` walks via `wp.e.Vertex.Inputs()` where `wp.e.Vertex` is
the **new build's wrapper** (not `state.vtx`):

[solver/jobs.go:864](../../../solver/jobs.go#L864)
```go
for _, inp := range e.Vertex.Inputs() {
    if err := j.walkProvenance(ctx, inp, f, visited); err != nil { ... }
}
```

So provenance reaches `D'`, hits `state[D']` with `op == nil`, and the
production guard either (pre-#5606) panics on `st.op.op` or (post-#5606)
silently skips the `ProvenanceProvider` call.

## The failing build does not need to use IgnoreCache

This is the important part: **the build whose `walkProvenance` panics does
not need to have `IgnoreCache` in its LLB**. The shift only requires
*some* prior or concurrent build to have asked for IgnoreCache on a vertex
whose digest collides with one the failing build references.

Concretely, three production scenarios all produce the same panic:

1. **Self-contained (the repro mode):** one client, two `gw.Solve` calls
   in one `c.Build`. Solve 1 has no IgnoreCache, Solve 2 does. Solve 2 is
   the one that crashes. The failing build's LLB *does* contain
   `IgnoreCache`.

2. **Inherited via the reuse path.** Daemon serves build A
   (`IgnoreCache=true` on some deep vertex), creating `state[D']`. While
   build A is still in flight (or its job is otherwise holding the state
   alive — see lifecycle below), build B comes in **with no IgnoreCache
   anywhere in its LLB** and loads the same deep vertex. Line 558 returns
   `state[D'].vtx` as `v`, build B's wrapper graph now references `D'`,
   and build B's `walkProvenance` hits the orphan even though build B's
   submitted LLB is clean.

3. **Concurrent races.** Build A and build B start nearly simultaneously.
   Build A's load runs first under `jl.mu`, performs the shift, creates
   `state[D']`. Build B's load runs next (no IgnoreCache), inherits via
   reuse path. Build B's `walkProvenance` runs before build A's scheduler
   resolves the op on `state[D']` (e.g., because build A's parent edge
   short-circuits too, or because A is still in `loadUnlocked`/early
   scheduling).

All three reach the same `walkProvenance hit nil shared op` condition.
Only #1 has `IgnoreCache` visible in the failing build's LLB.

### Lifecycle: when does state[D'] survive long enough to be inherited?

`state[D']` is held in `actives` while:

- some job has it in `state.jobs`, OR
- some parent state has it in `state.parents` (`state.childVtx` on the
  parent side).

`Job.Discard` ([solver/jobs.go:877](../../../solver/jobs.go#L877)) removes
its job from every state and calls `deleteIfUnreferenced`. For a
job-level top-level load (`parent == nil`), `state[D']` has empty
`parents`, so once the IgnoreCache job is discarded, `state[D']` is GC'd.

But the orphan persists across the IgnoreCache job's *lifetime*, which is
the entire `c.Build` plus any provenance/export work after solve. That
window can easily overlap with a sibling build on the same daemon.

## A second flavor of the same bug: `op != nil`, `op.op == nil`

The production guard is `st.op != nil && st.op.op != nil` — two checks.
The orphan above trips the **first** check. There's a separate path that
trips only the **second**:

[solver/jobs.go:288](../../../solver/jobs.go#L288), inside `addJobs`:

```go
mergedInputEdge := inputState.getEdge(inputEdge.Index)
```

This is called during edge-merge propagation. `state.getEdge` creates a
`sharedOp` on `inputState` (sets `op` to a fresh `sharedOp`) but does
**not** call `sharedOp.getOp()` to run the resolver. The resolver only
runs lazily inside `opOnce.Do` from `CacheMap`, `Exec`, `Cache`, etc.
([solver/jobs.go:1319](../../../solver/jobs.go#L1319)).

So `addJobs` can leave `state[X].op` non-nil but `state[X].op.op == nil`.
If `walkProvenance` later reaches such a state, the second guard catches
it. Pre-guard, this path also panics — same `addr=0x50` because that's the
offset of `op` inside `sharedOp`, and `(*sharedOp)(s.op).op` is the nil
deref.

This flavor doesn't depend on IgnoreCache at all. It requires an edge
merge (cache-key match between two edges) where the merged-into target's
input chain reaches a state that hasn't independently been scheduled to
the point of CacheMap.

## Reproducer (`ignorecache-shift` mode)

```
mkChain(deepIgnore bool):
  deep := base.Run("sh -c true")        // optionally + llb.IgnoreCache when deepIgnore
  intermediate := scratch.File(Copy(deep, "/", "/x"))
  return Merge([base, intermediate])
```

The harness ([main.go:810](main.go#L810)):

1. `gw.Solve(noIgnoreDef)` → `ref1.Evaluate()` so the no-ignore root edge
   becomes `Complete`. This populates `state[deep_D]`, `state[mid_M]`,
   `state[root_R]`, all with their `vtx.Inputs()` referencing `D`.
2. `gw.Solve(withIgnoreDef)`. Load shifts deep to `D'`. The wrappers for
   intermediate and root are *fresh* `vertexWithCacheOptions` referencing
   `D'`, but `state[mid_M]` and `state[root_R]` are reused — their `vtx`
   still references `D`.
3. `scheduler.build(root edge)` finds `state[root_R].edges[0]` already
   complete from step 1. `unpark` returns done, never calls
   `createInputRequests`. `state[D'].getEdge` is never called.
4. `res2.Ref.Evaluate` triggers `captureProvenance` →
   `withProvenance.WalkProvenance` → `Job.walkProvenance` walks via the
   second build's wrapper graph → reaches `D'` → orphan → **panic**.

### Run

```sh
# Build pre-guard buildkitd
cd /src/.worktree-claude/pre-guard
go build -o /tmp/buildkitd-preguard-clean ./cmd/buildkitd/

# Run it
mkdir -p /tmp/bk-state /tmp/bk-logs
/tmp/buildkitd-preguard-clean --root /tmp/bk-state \
  --addr unix:///tmp/buildkitd.sock --debug 2>/tmp/bk-logs/daemon.log &

# Trigger panic
cd /src/hack/repro/provenance-race
go build .
BUILDKIT_HOST=unix:///tmp/buildkitd.sock \
  ./provenance-race -mode ignorecache-shift -iterations 1 -parallel 1
```

The daemon dies on the first iteration with:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x50 pc=...]

goroutine N [running]:
github.com/moby/buildkit/solver.(*Job).walkProvenance(...)
    /src/.worktree-claude/pre-guard/solver/jobs.go:737
github.com/moby/buildkit/solver.(*Job).walkProvenance(...)
    /src/.worktree-claude/pre-guard/solver/jobs.go:746
...
github.com/moby/buildkit/solver.(*withProvenance).WalkProvenance(...)
github.com/moby/buildkit/solver/llbsolver.captureProvenance(...)
    /src/.worktree-claude/pre-guard/solver/llbsolver/provenance.go:276
github.com/moby/buildkit/solver/llbsolver.(*resultProxy).Result.func2(...)
    /src/.worktree-claude/pre-guard/solver/llbsolver/bridge.go:331
github.com/moby/buildkit/util/flightcontrol.(*call[...]).run(...)
sync.(*Once).doSlow(...)
sync.(*Once).Do(...)
```

The address `0x50` is the offset of the `op` field inside `sharedOp` —
matches the panic in PR #5606 byte-for-byte.

### Why the first iteration is slow and subsequent iterations are instant

The bug fires on every iteration; what changes is everything around it.

The first `gw.Solve` in the harness must execute its op chain before the
second can race against it. On a cold daemon that means: resolve
`busybox:latest` from the registry (manifest fetch, possibly layer pulls),
run `sh -c true` in a real container, snapshot the result, compute cache
keys. Even though `sh -c true` itself is trivial, the registry round-trip
and snapshot bookkeeping take hundreds of ms.

On subsequent iterations, BuildKit's persistent cache (`cache.db`,
`history.db`) recognizes the same cache key and the no-IgnoreCache solve
short-circuits — every op completes from cache without running. The
actives-map shift, the orphan creation, and the `walkProvenance` walk all
happen at memory speed.

So a 586ms first run isn't even cold — it's already warm-ish. A truly
cold daemon takes a few seconds; a fully warm daemon takes well under
100ms per iteration. The bug fires identically in both.

## Behavior on the current (post-guard) branch

The guard at
[solver/jobs.go:854](../../../solver/jobs.go#L854) skips the type
assertion when `st.op == nil` or `st.op.op == nil`. The instrumentation
in this branch converts the silent skip into a returned error so the
failure mode is visible:

```
walkProvenance visiting active state has_resolved_op=false has_shared_op=false
  vertex_name="[repro 0/1] ignorecache-shift deep"
walkProvenance hit nil shared op for [repro 0/1] ignorecache-shift deep
```

Same vertex, same condition — but the daemon stays alive because the
guard prevents the dereference. Provenance for the deep vertex is
silently missing from the attestation.

**Note:** if the repro reports `completed 1 ignorecache-shift iterations
in <Nms>` with no error, the daemon is the **system** `buildkitd`, not
the instrumented build in the working copy. Verify with:

```sh
strings /usr/local/bin/buildkitd | grep "walkProvenance hit"
```

Empty output means you're hitting an un-instrumented binary.
`make && sudo make install` rebuilds and reinstalls, then restart the
daemon.

## Implications

- The guard added in #5606 is necessary. The race is reachable from a
  single client `c.Build` call, and also from a multi-client setup where
  the failing client never used IgnoreCache.
- Returning an error from `walkProvenance` (rather than silently skipping)
  would make the issue visible to users — they'd see "failed to capture
  provenance" instead of getting silently incomplete provenance for the
  affected vertex.
- A real fix has to address the wrapper-graph-vs-state-graph divergence
  that lets `walkProvenance` reach a state the scheduler never visited.

## Fix space

**Cheapest — walk via `state.vtx.Inputs()` instead of `e.Vertex.Inputs()`
in `walkProvenance`.** Once a digest is resolved to a `state`, follow that
state's input chain (which corresponds to the load that registered the
state and whose ops actually got scheduled), not the caller's wrapper
inputs. ~3 lines. Trade-off: provenance no longer reflects per-build
metadata that diverges from `state.vtx` (e.g., `IgnoreCache=true` on the
failing build's wrapper) — arguably more correct because that op didn't
actually run for this build, the cached result from a sibling build did.

**Middle — eagerly populate `state[D'].op` at shift time.** When
`loadUnlocked` decides to shift, if `actives[D]` already has a resolved
`sharedOp`, share or recreate it on the new state. More invasive because
`sharedOp` holds back-references to `*state`. Preserves the wrapper-graph
walk semantics. Doesn't help the `addJobs` flavor.

**Real fix — don't shift when the parent edge is already Complete.** The
shift exists so `IgnoreCache=true` can force a re-run. If the parent's
cached complete edge is going to short-circuit anyway, the shift produces
an orphan whose only effect is to break provenance. But "is the parent
edge complete" is a scheduler-time question that load doesn't have a
clean answer to without restructuring.

## Issue #6731 is the same race, observed through a different field

[#6731](https://github.com/moby/buildkit/issues/6731) reports an
intermittent `failed to capture provenance: failed to parse image digest
: invalid checksum digest format` on the **current** (post-#5606) branch.
The empty digest comes from `digest.Parse("")` inside
`ImageIdentifier.Capture`
([source/containerimage/identifier.go:43-47](../../../source/containerimage/identifier.go#L43-L47)),
called via `captureProvenance` ->
[provenance.go:316-317](../../../solver/llbsolver/provenance.go#L316-L317):

```go
case *ops.SourceOp:
    id, pin := op.Pin()
    err := id.Capture(c, pin)
```

The pin comes from `SourceOp.Pin()`
([ops/source.go:54-56](../../../solver/llbsolver/ops/source.go#L54-L56))
which returns `(s.id, s.pin)`. `s.id` is written by `s.instance(...)` at
the start of `SourceOp.CacheMap`; `s.pin` is written further down, after
`src.CacheKey(...)` returns
([ops/source.go:88-89](../../../solver/llbsolver/ops/source.go#L88-L89)).
So between those two writes, a concurrent reader sees `id != nil` and
`pin == ""` — the exact shape that bypasses the post-#5606 guard (which
only checks `op != nil && op.op != nil`) and crashes
`ImageIdentifier.Capture` with the reported error.

**This is the same race as the #5606 panic**, just sliced through a
later instant: instead of catching `state[X]` with `op == nil` (the
orphan), it catches `state[X]` with `op.op != nil` but `op.op.pin == ""`
(the resolved-but-not-cache-keyed window). Both require the same
underlying condition: a build's `walkProvenance` reaches a `state[X]`
that another build is still initializing. The IgnoreCache shift is the
mechanism that produces both.

### How the shift produces the empty-pin race

Three solves in one `c.Build`, sharing the actives map, with a SourceOp
sleep that holds the post-instance-pre-CacheKey window open:

1. **warmup** — plain LLB, no IgnoreCache. Synchronously evaluates so
   the no-ignore root edge is `Complete` and `state[base_image_D]`,
   `state[mid_M]`, `state[root_R]` are all populated with resolved ops
   and set pins.

2. **race-creator** — IgnoreCache on the base image, with a *unique*
   intermediate vertex (different copy destination, so its LLB digest
   differs from warmup's mid). Async evaluate. Load triggers the shift:
   `state[base_image_D']` is created at `actives[D']` (where
   `D' = digest("D-ignorecache")`). Because this build's mid digest is
   fresh, its load creates a *fresh* `state[mid_M2]` whose
   `vtx.Inputs()[0]` references `D'`. The fresh `state[root_R2]` is also
   fresh. Scheduler runs on the fresh root, walks down through fresh
   mid, calls `state[base_image_D'].getEdge` — populating
   `state[base_image_D'].op` with a fresh `sharedOp`. The scheduler
   triggers `CacheMap` on `state[base_image_D']`; inside,
   `sharedOp.getOp()` runs the resolver (sets `state.op.op = &SourceOp{}`),
   and then `SourceOp.CacheMap` calls `instance()` (sets `s.id`). At
   this point the `BUILDKIT_REPRO_DELAY_SOURCE_PIN` sleep holds the
   goroutine **before** `src.CacheKey` writes `s.pin`.

3. **walker** — IgnoreCache on the base image, with the *same* mid and
   root LLB as warmup. Synchronous evaluate after a short head-start so
   step 2 is guaranteed to be in the sleep window. Load:
    - `load(base_ic)`: `actives[D']` already exists (created by
      race-creator). Line 558 reuse path: `v = state[D'].vtx`. Returns
      a wrapper at `D'`.
    - `load(mid)`: digest matches warmup's mid (no fresh component), so
      `actives[M]` exists. Reuse `state[mid_M]` — but the *new wrapper*
      stored in this load's return value has `Inputs()[0]` pointing at
      `D'` (because the recursive load returned the `D'` wrapper).
      `state[mid_M].vtx` is unchanged; it still has Inputs pointing at
      `D` (warmup's view).
    - `load(root)`: same digest as warmup (`R`); reuse `state[root_R]`.
    - The walker's returned wrapper for the root has `Inputs()[0] = D'`
      (and the mid input). State `vtx`s are unchanged.

   `scheduler.build(walker_root_edge)` keys by digest `R`, finds
   `state[root_R].edges[0]` already complete from step 1, returns
   immediately. `Build` returns. `captureProvenance` runs.
   `walkProvenance` follows the **walker's wrapper graph**, which goes
   `R -> D' -> ...`, so it visits `state[base_image_D']` —
   currently mid-CacheMap with `op.op != nil` but `pin == ""`. The
   guard passes (`op != nil && op.op != nil`), `f(SourceOp)` is called,
   `SourceOp.Pin()` returns `(id, "")`, and
   `digest.Parse("")` returns `"invalid checksum digest format"`.
   `captureProvenance` returns the wrapped error;
   `resultProxy.Result` returns it; the client sees:

```
failed to capture provenance: failed to parse image digest : invalid checksum digest format
```

This fires **without** the local error-return instrumentation in
`solver/jobs.go` — only the `BUILDKIT_REPRO_DELAY_SOURCE_PIN` sleep is
needed, and that is purely timing (no logic change to the guard).

### Confirmation that the actives key is the shifted digest

Manually computed: `sha256("sha256:<warmup_image_dgst>-ignorecache")`
matches the digest under which the daemon log records the shifted
state's vtx. For the busybox source from the run that produced this
analysis:

- warmup image: `sha256:22700c910cfcb723cdf2fcc0f17452030417956f4d8bf13f6dfddec7681d7180`
- shifted (`D'`): `sha256:f5c3837372f7819948b9fd893eb5d307f6b21ea9de6e030ac44209fd5ff7204e`
- `printf 'sha256:22700c910cfcb...-ignorecache' | sha256sum` = `f5c3837372f78199...`

Confirms `D' = dgstWithoutCache(warmup_image_dgst)`. The daemon log
reports the shifted-state's vertex digest as `D'` because
`vertexWithCacheOptions.Digest()` returns the shifted `dgst`, not the
underlying LLB digest.

### Run

```sh
# Build & install instrumented daemon (env-var sleep in SourceOp.CacheMap)
cd /src
make && sudo make install
sudo pkill -f /usr/local/bin/buildkitd

mkdir -p /tmp/bk-pin-race-state /tmp/bk-pin-race-logs
BUILDKIT_REPRO_DELAY_SOURCE_PIN=5s \
BUILDKIT_REPRO_DELAY_SOURCE_PIN_NAME=docker-image \
  /usr/local/bin/buildkitd \
    --root /tmp/bk-pin-race-state \
    --addr unix:///tmp/bk-pin-race.sock \
    --debug 2>/tmp/bk-pin-race-logs/daemon.log &

cd /src/hack/repro/provenance-race
go build .
BUILDKIT_HOST=unix:///tmp/bk-pin-race.sock \
  ./provenance-race -mode pin-race -iterations 1 -parallel 1
```

Expected client output (every iteration):

```
repro failed after ~22s: failed to capture provenance: failed to parse image digest : invalid checksum digest format
```

The 22 seconds is the warmup's two `SourceOp.CacheMap` calls (5s sleep
each, plus overhead) + the 500ms head-start + the walker's walk during
the race-creator's final 5s sleep.

### Fix space, restated for the empty-pin variant

The two cheaper fixes ([Fix space](#fix-space)) handle this variant for
free:

- **Walk via `state.vtx.Inputs()`** would never visit `state[D']` from
  the walker (since walker reuses `state[mid_M]` whose `vtx.Inputs()[0]`
  is `D`, not `D'`). The empty-pin race vanishes alongside the orphan
  panic.
- **Eagerly populate `state[D'].op`** doesn't help here, because
  `op.op` would *still* be set during the race-creator's CacheMap and
  the walker would *still* read the partial SourceOp. So this fix
  closes #5606 but not #6731.

That asymmetry is itself useful: it tells us that fix #1 is strictly
better for the user-visible behavior, since it covers both ways the
divergence surfaces.

## Modes in the harness

`fanout`, `same-cache-source`, `merge-extra-hosts`, `dalec-mergeatpath`,
`gateway-dalec-mergeatpath`, `double-copylink-exec`,
`double-copylink-exec-delayroot`, `double-merge-exec`,
`double-merge-file`, `copylink`, `multi-ref-overlap`,
`multi-ref-fanout`, `stutter-evaluate`, `deep-tostate-chain`,
**`ignorecache-shift`** (deterministic on a pre-guard daemon —
reproduces the #5606 panic), **`pin-race`** (deterministic on a
post-guard daemon with `BUILDKIT_REPRO_DELAY_SOURCE_PIN` — reproduces
the #6731 client-visible empty-pin error).
