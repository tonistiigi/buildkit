package worker

import (
	"context"
	"io"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver-next"
	digest "github.com/opencontainers/go-digest"
)

type Worker interface {
	// ID needs to be unique in the cluster
	ID() string
	Labels() map[string]string
	LoadRef(id string) (cache.ImmutableRef, error)
	// InstructionCache() instructioncache.InstructionCache
	// ResolveOp resolves Vertex.Sys() to Op implementation. SubBuilder is needed for pb.Op_Build.
	// FIXME
	ResolveOp(v solver.Vertex, s frontend.FrontendLLBBridge) (solver.Op, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
	// Exec is similar to executor.Exec but without []mount.Mount
	Exec(ctx context.Context, meta executor.Meta, rootFS cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error
	DiskUsage(ctx context.Context, opt client.DiskUsageInfo) ([]*client.UsageInfo, error)
	Exporter(name string) (exporter.Exporter, error)
	Prune(ctx context.Context, ch chan client.UsageInfo) error
	GetRemote(ctx context.Context, ref cache.ImmutableRef) (*solver.Remote, error)
}

// Pre-defined label keys
const (
	labelPrefix      = "org.mobyproject.buildkit.worker."
	LabelOS          = labelPrefix + "os"          // GOOS
	LabelArch        = labelPrefix + "arch"        // GOARCH
	LabelExecutor    = labelPrefix + "executor"    // "oci" or "containerd"
	LabelSnapshotter = labelPrefix + "snapshotter" // containerd snapshotter name ("overlay", "native", ...)
	LabelHostname    = labelPrefix + "hostname"
)
