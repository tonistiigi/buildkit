package cacheimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/images"
	"github.com/docker/distribution/manifest"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/push"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const mediaTypeConfig = "application/vnd.buildkit.cacheconfig.v0"

type ExporterOpt struct {
	Snapshotter    snapshot.Snapshotter
	ContentStore   content.Store
	Differ         diff.Comparer
	SessionManager *session.Manager
}

func NewCacheExporter(opt ExporterOpt) *CacheExporter {
	return &CacheExporter{opt: opt}
}

type CacheExporter struct {
	opt ExporterOpt
}

func (ce *CacheExporter) ExporterForTarget(target string) *RegistryCacheExporter {
	return &RegistryCacheExporter{target: target, exporter: ce}
}

func (ce *CacheExporter) Export(ctx context.Context, recs []solver.ExportRecord, target string) error {
	// own type because oci type can't be pushed and docker type doesn't have annotations
	type manifestList struct {
		manifest.Versioned

		// Manifests references platform specific manifests.
		Manifests []ocispec.Descriptor `json:"manifests"`
	}

	configs := map[digest.Digest]*configItem{}

	var mfst manifestList
	mfst.SchemaVersion = 2
	mfst.MediaType = images.MediaTypeDockerSchema2ManifestList

	allBlobs := map[digest.Digest]struct{}{}

	mp := contentutil.NewMultiProvider(ce.opt.ContentStore)

	for _, r := range recs {

		if r.Remote != nil && len(r.Remote.Descriptors) > 0 {

			for i, desc := range r.Remote.Descriptors {
				if _, ok := allBlobs[desc.Digest]; ok {
					continue
				}
				allBlobs[desc.Digest] = struct{}{}
				mfst.Manifests = append(mfst.Manifests, desc)
				mp.Add(desc.Digest, r.Remote.Provider)

				config, ok := configs[desc.Digest]
				if !ok {
					config = &configItem{Digest: desc.Digest, Links: map[solver.CacheLink]struct{}{}}
					configs[desc.Digest] = config
				}
				if i+1 < len(r.Remote.Descriptors) {
					config.Parent = r.Remote.Descriptors[i+1].Digest
				}
			}

		}

		config, ok := configs[r.Digest]
		if !ok {
			config = &configItem{Digest: r.Digest, Links: map[solver.CacheLink]struct{}{}}
			configs[r.Digest] = config
		}

		for l := range r.Links {
			config.Links[l] = struct{}{}
		}
	}

	configList := make([]configItemJSON, 0, len(configs))

	for _, cfg := range configs {
		if cfg.Parent == "" && len(cfg.Links) == 0 {
			continue
		}
		cj := configItemJSON{
			Digest: cfg.Digest,
			Parent: cfg.Parent,
		}
		for l := range cfg.Links {
			cj.Links = append(cj.Links, l)
		}
		configList = append(configList, cj)
	}

	dt, err := json.Marshal(configList)
	if err != nil {
		return err
	}

	dgst := digest.FromBytes(dt)

	addAsRoot := content.WithLabels(map[string]string{
		"containerd.io/gc.root": time.Now().UTC().Format(time.RFC3339Nano),
	})

	configDone := oneOffProgress(ctx, fmt.Sprintf("writing config %s", dgst))
	// TODO: use inMemoryProvider
	if err := content.WriteBlob(ctx, ce.opt.ContentStore, dgst.String(), bytes.NewReader(dt), int64(len(dt)), dgst, addAsRoot); err != nil {
		return configDone(errors.Wrap(err, "error writing config blob"))
	}
	configDone(nil)

	mfst.Manifests = append(mfst.Manifests, ocispec.Descriptor{
		MediaType: mediaTypeConfig,
		Size:      int64(len(dt)),
		Digest:    dgst,
	})

	dt, err = json.Marshal(mfst)
	if err != nil {
		return errors.Wrap(err, "failed to marshal manifest")
	}

	dgst = digest.FromBytes(dt)

	mfstDone := oneOffProgress(ctx, fmt.Sprintf("writing manifest %s", dgst))
	if err := content.WriteBlob(ctx, ce.opt.ContentStore, dgst.String(), bytes.NewReader(dt), int64(len(dt)), dgst, addAsRoot); err != nil {
		return mfstDone(errors.Wrap(err, "error writing manifest blob"))
	}
	mfstDone(nil)

	return push.Push(ctx, ce.opt.SessionManager, ce.opt.ContentStore, dgst, target, false)
}

type configItem struct {
	Digest digest.Digest
	Parent digest.Digest
	Links  map[solver.CacheLink]struct{}
}

type configItemJSON struct {
	Digest digest.Digest
	Parent digest.Digest `json:",omitempty"`
	Links  []solver.CacheLink
}

type RegistryCacheExporter struct {
	exporter *CacheExporter
	target   string
}

func (ce *RegistryCacheExporter) Export(ctx context.Context, recs []solver.ExportRecord) error {
	return ce.exporter.Export(ctx, recs, ce.target)
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}
