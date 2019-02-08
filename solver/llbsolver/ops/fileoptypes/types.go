package fileoptypes

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
)

type Ref interface {
	Release(context.Context) error
}

type Mount interface {
	Release(context.Context) error
	IsFileOpMount()
}

type Backend interface {
	Mkdir(context.Context, Mount, pb.FileActionMkDir) error
	Mkfile(context.Context, Mount, pb.FileActionMkFile) error
	Rm(context.Context, Mount, pb.FileActionRm) error
	Copy(context.Context, Mount, Mount, pb.FileActionCopy) error
}

type RefManager interface {
	Prepare(ctx context.Context, ref Ref, readonly bool) (Mount, error)
	Commit(ctx context.Context, mount Mount) (Ref, error)
}
