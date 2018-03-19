package cacheimport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type ImportOpt struct {
	SessionManager *session.Manager
	Worker         worker.Worker // TODO: remove. This sets the worker where the cache is imported to. Should be passed on load instead.
}

func NewCacheImporter(opt ImportOpt) *CacheImporter {
	return &CacheImporter{opt: opt}
}

type CacheImporter struct {
	opt ImportOpt
}

type cacheKeyStorage struct {
	allLayers map[digest.Digest]ocispec.Descriptor
	byID      map[string]*parsedConfigItem
}

func (cs *cacheKeyStorage) Exists(id string) bool {
	_, ok := cs.byID[id]
	return ok
}

func (cs *cacheKeyStorage) Walk(func(id string) error) error {
	return nil
}

func (cs *cacheKeyStorage) WalkResults(id string, fn func(solver.CacheResult) error) error {
	_, ok := cs.byID[id]
	if !ok {
		return nil
	}
	return fn(solver.CacheResult{ID: id}) // TODO: CreatedAt
}

func (cs *cacheKeyStorage) Load(id string, resultID string) (solver.CacheResult, error) {
	_, ok := cs.byID[id]
	if !ok {
		return solver.CacheResult{}, nil
	}
	return solver.CacheResult{ID: id}, nil
}

func (cs *cacheKeyStorage) AddResult(id string, res solver.CacheResult) error {
	return nil
}

func (cs *cacheKeyStorage) Release(resultID string) error {
	return nil
}
func (cs *cacheKeyStorage) AddLink(id string, link solver.CacheInfoLink, target string) error {
	return nil
}
func (cs *cacheKeyStorage) WalkLinks(id string, link solver.CacheInfoLink, fn func(id string) error) error {
	c, ok := cs.byID[id]
	if !ok {
		return nil
	}
	for l := range c.Links {
		if l.Input == link.Input && l.Base == link.Digest && l.Output == link.Output && l.Selector == link.Selector {
			if err := fn(l.Target.String()); err != nil {
				return err
			}
		}
	}
	return nil
}

type cacheResultStorage struct {
	w         worker.Worker
	allLayers map[digest.Digest]ocispec.Descriptor
	byID      map[string]*parsedConfigItem
	f         remotes.Fetcher
}

func (cs *cacheResultStorage) Save(res solver.Result) (solver.CacheResult, error) {
	return solver.CacheResult{}, errors.Errorf("importer is immutable")
}

func (cs *cacheResultStorage) Load(ctx context.Context, res solver.CacheResult) (solver.Result, error) {
	remote, err := cs.LoadRemote(ctx, res)
	if err != nil {
		return nil, err
	}
	ref, err := cs.w.FromRemote(ctx, remote)
	if err != nil {
		return nil, err
	}
	return worker.NewWorkerRefResult(ref, cs.w), nil
}

func (cs *cacheResultStorage) LoadRemote(ctx context.Context, res solver.CacheResult) (*solver.Remote, error) {
	var descs []ocispec.Descriptor
	provider := contentutil.NewMultiProvider(nil)

	dgst := digest.Digest(res.ID)

	for {
		if dgst == "" {
			break
		}
		desc, ok := cs.allLayers[dgst]
		if !ok {
			return nil, errors.WithStack(solver.ErrNotFound)
		}
		provider.Add(desc.Digest, contentutil.FromFetcher(cs.f, desc))
		descs = append(descs, desc)
		cfg, ok := cs.byID[dgst.String()]
		if !ok {
			break
		}
		dgst = cfg.Parent
	}

	if len(descs) == 0 {
		return nil, errors.WithStack(solver.ErrNotFound)
	}

	// reverse
	for left, right := 0, len(descs)-1; left < right; left, right = left+1, right-1 {
		descs[left], descs[right] = descs[right], descs[left]
	}

	return &solver.Remote{
		Descriptors: descs,
		Provider:    provider,
	}, nil
}

func (cs *cacheResultStorage) Exists(id string) bool {
	_, ok := cs.allLayers[digest.Digest(id)]
	return ok
}

func (ci *CacheImporter) getCredentialsFromSession(ctx context.Context) func(string) (string, string, error) {
	id := session.FromContext(ctx)
	if id == "" {
		return nil
	}

	return func(host string) (string, string, error) {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		caller, err := ci.opt.SessionManager.Get(timeoutCtx, id)
		if err != nil {
			return "", "", err
		}

		return auth.CredentialsFunc(context.TODO(), caller)(host)
	}
}

func (ci *CacheImporter) Resolve(ctx context.Context, ref string) (solver.CacheManager, error) {
	resolver := docker.NewResolver(docker.ResolverOptions{
		Client:      http.DefaultClient,
		Credentials: ci.getCredentialsFromSession(ctx),
	})

	ref, desc, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}

	fetcher, err := resolver.Fetcher(ctx, ref)
	if err != nil {
		return nil, err
	}

	b := contentutil.NewBuffer()

	// TODO: do this directly
	if _, err := remotes.FetchHandler(b, fetcher)(ctx, desc); err != nil {
		return nil, err
	}

	dt, err := content.ReadBlob(ctx, b, desc.Digest)
	if err != nil {
		return nil, err
	}

	var mfst ocispec.Index
	if err := json.Unmarshal(dt, &mfst); err != nil {
		return nil, err
	}

	allLayers := map[digest.Digest]ocispec.Descriptor{}

	var configDesc ocispec.Descriptor

	for _, m := range mfst.Manifests {
		if m.MediaType == mediaTypeConfig {
			configDesc = m
			continue
		}
		allLayers[m.Digest] = m
	}

	if configDesc.Digest == "" {
		return nil, errors.Errorf("invalid build cache from %s", ref)
	}

	if _, err := remotes.FetchHandler(b, fetcher)(ctx, configDesc); err != nil {
		return nil, err
	}

	dt, err = content.ReadBlob(ctx, b, configDesc.Digest)
	if err != nil {
		return nil, err
	}

	var cfgJSONs []configItemJSON
	if err := json.Unmarshal(dt, &cfgJSONs); err != nil {
		return nil, err
	}

	configs := map[string]*parsedConfigItem{}
	for _, ci := range cfgJSONs {
		configs[ci.Digest.String()] = &parsedConfigItem{
			Digest: ci.Digest,
			Parent: ci.Parent,
			Links:  map[reverseCacheLink]struct{}{},
		}
	}

	for _, ci := range cfgJSONs {
		for _, l := range ci.Links {
			if l.Source == "" {
				id := digest.FromBytes([]byte(fmt.Sprintf("%s@%d", l.Base, l.Output))).String()
				configs[id] = configs[ci.Digest.String()]
				continue
			}
			configs[l.Source.String()].Links[reverseCacheLink{
				Target:   ci.Digest,
				Input:    l.Input,
				Output:   l.Output,
				Base:     l.Base,
				Selector: l.Selector,
			}] = struct{}{}
		}
	}

	keysStorage := &cacheKeyStorage{
		allLayers: allLayers,
		byID:      configs,
	}

	resultStorage := &cacheResultStorage{
		w:         ci.opt.Worker,
		f:         fetcher,
		allLayers: allLayers,
		byID:      configs,
	}

	return solver.NewCacheManager(ref, keysStorage, resultStorage), nil
}

func notifyStarted(ctx context.Context, v *client.Vertex) {
	pw, _, _ := progress.FromContext(ctx)
	defer pw.Close()
	now := time.Now()
	v.Started = &now
	v.Completed = nil
	pw.Write(v.Digest.String(), *v)
}

func notifyCompleted(ctx context.Context, v *client.Vertex, err error) {
	pw, _, _ := progress.FromContext(ctx)
	defer pw.Close()
	now := time.Now()
	if v.Started == nil {
		v.Started = &now
	}
	v.Completed = &now
	v.Cached = false
	if err != nil {
		v.Error = err.Error()
	}
	pw.Write(v.Digest.String(), *v)
}

type parsedConfigItem struct {
	Digest digest.Digest
	Parent digest.Digest
	Links  map[reverseCacheLink]struct{}
}

type reverseCacheLink struct {
	Target   digest.Digest
	Input    solver.Index
	Output   solver.Index
	Base     digest.Digest
	Selector digest.Digest
}
