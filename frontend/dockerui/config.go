package dockerui

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"strconv"
	"strings"

	"github.com/containerd/containerd/platforms"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/image"
	"github.com/moby/buildkit/frontend/dockerfile/dockerignore"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

const (
	buildArgPrefix      = "build-arg:"
	labelPrefix         = "label:"
	contextPrefix       = "context:"
	inputMetadataPrefix = "input-metadata:"

	keyTarget           = "target"
	keyCgroupParent     = "cgroup-parent"
	keyForceNetwork     = "force-network-mode"
	keyGlobalAddHosts   = "add-hosts"
	keyHostname         = "hostname"
	keyImageResolveMode = "image-resolve-mode"
	keyMultiPlatform    = "multi-platform"
	keyNoCache          = "no-cache"
	keyShmSize          = "shm-size"
	keyTargetPlatform   = "platform"
	keyUlimit           = "ulimit"
	keyCacheFrom        = "cache-from"    // for registry only. deprecated in favor of keyCacheImports
	keyCacheImports     = "cache-imports" // JSON representation of []CacheOptionsEntry

	keyCacheNSArg       = "build-arg:BUILDKIT_CACHE_MOUNT_NS"
	keyMultiPlatformArg = "build-arg:BUILDKIT_MULTI_PLATFORM"
	keyHostnameArg      = "build-arg:BUILDKIT_SANDBOX_HOSTNAME"
)

type BuildConfig struct {
	client client.Client

	BuildArgs        map[string]string
	CacheIDNamespace string
	CgroupParent     string
	ExtraHosts       []llb.HostIP
	Hostname         string
	ImageResolveMode llb.ResolveMode
	Labels           map[string]string
	NetworkMode      pb.NetMode
	ShmSize          int64
	Target           string
	Ulimits          []pb.Ulimit

	CacheImports           []client.CacheOptionsEntry
	TargetPlatforms        []ocispecs.Platform // nil means default
	BuildPlatforms         []ocispecs.Platform
	MultiPlatformRequested bool

	ignoreCache []string
	bctx        *buildContext
	g           flightcontrol.Group
	bopts       client.BuildOpts

	dockerignore  []byte
	defaultVertex digest.Digest
}

type ContextOpt struct {
	UseDockerignore bool
	LocalOpts       []llb.LocalOption
	Platform        *ocispecs.Platform
}

func validateMinCaps(c client.Client) error {
	opts := c.BuildOpts().Opts
	caps := c.BuildOpts().LLBCaps

	if err := caps.Supports(pb.CapFileBase); err != nil {
		return errors.Wrap(err, "needs BuildKit 0.5 or later")
	}
	if opts["override-copy-image"] != "" {
		return errors.New("support for \"override-copy-image\" was removed in BuildKit 0.11")
	}
	if v, ok := opts["build-arg:BUILDKIT_DISABLE_FILEOP"]; ok {
		if b, err := strconv.ParseBool(v); err == nil && b {
			return errors.New("support for \"BUILDKIT_DISABLE_FILEOP\" build-arg was removed in BuildKit 0.11")
		}
	}
	return nil
}

func NewBuildConfig(c client.Client) (*BuildConfig, error) {
	if err := validateMinCaps(c); err != nil {
		return nil, err
	}

	bc := &BuildConfig{
		client: c,
		bopts:  c.BuildOpts(), // avoid grpc on every call
	}

	if err := bc.init(); err != nil {
		return nil, err
	}

	return bc, nil
}

func (b *BuildConfig) init() error {
	opts := b.bopts.Opts

	defaultBuildPlatform := platforms.Normalize(platforms.DefaultSpec())
	if workers := b.bopts.Workers; len(workers) > 0 && len(workers[0].Platforms) > 0 {
		defaultBuildPlatform = workers[0].Platforms[0]
	}
	buildPlatforms := []ocispecs.Platform{defaultBuildPlatform}
	targetPlatforms := []ocispecs.Platform{}
	if v := opts[keyTargetPlatform]; v != "" {
		var err error
		targetPlatforms, err = parsePlatforms(v)
		if err != nil {
			return err
		}
	}
	b.BuildPlatforms = buildPlatforms
	b.TargetPlatforms = targetPlatforms

	resolveMode, err := parseResolveMode(opts[keyImageResolveMode])
	if err != nil {
		return err
	}
	b.ImageResolveMode = resolveMode

	extraHosts, err := parseExtraHosts(opts[keyGlobalAddHosts])
	if err != nil {
		return errors.Wrap(err, "failed to parse additional hosts")
	}
	b.ExtraHosts = extraHosts

	shmSize, err := parseShmSize(opts[keyShmSize])
	if err != nil {
		return errors.Wrap(err, "failed to parse shm size")
	}
	b.ShmSize = shmSize

	ulimits, err := parseUlimits(opts[keyUlimit])
	if err != nil {
		return errors.Wrap(err, "failed to parse ulimit")
	}
	b.Ulimits = ulimits

	defaultNetMode, err := parseNetMode(opts[keyForceNetwork])
	if err != nil {
		return err
	}
	b.NetworkMode = defaultNetMode

	var ignoreCache []string
	if v, ok := opts[keyNoCache]; ok {
		if v == "" {
			ignoreCache = []string{} // means all stages
		} else {
			ignoreCache = strings.Split(v, ",")
		}
	}
	b.ignoreCache = ignoreCache

	multiPlatform := len(targetPlatforms) > 1
	if v := opts[keyMultiPlatformArg]; v != "" {
		opts[keyMultiPlatform] = v
	}
	if v := opts[keyMultiPlatform]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.Errorf("invalid boolean value for multi-platform: %s", v)
		}
		if !b && multiPlatform {
			return errors.Errorf("conflicting config: returning multiple target platforms is not allowed")
		}
		multiPlatform = b
	}
	b.MultiPlatformRequested = multiPlatform

	var cacheImports []client.CacheOptionsEntry
	// new API
	if cacheImportsStr := opts[keyCacheImports]; cacheImportsStr != "" {
		var cacheImportsUM []controlapi.CacheOptionsEntry
		if err := json.Unmarshal([]byte(cacheImportsStr), &cacheImportsUM); err != nil {
			return errors.Wrapf(err, "failed to unmarshal %s (%q)", keyCacheImports, cacheImportsStr)
		}
		for _, um := range cacheImportsUM {
			cacheImports = append(cacheImports, client.CacheOptionsEntry{Type: um.Type, Attrs: um.Attrs})
		}
	}
	// old API
	if cacheFromStr := opts[keyCacheFrom]; cacheFromStr != "" {
		cacheFrom := strings.Split(cacheFromStr, ",")
		for _, s := range cacheFrom {
			im := client.CacheOptionsEntry{
				Type: "registry",
				Attrs: map[string]string{
					"ref": s,
				},
			}
			// FIXME(AkihiroSuda): skip append if already exists
			cacheImports = append(cacheImports, im)
		}
	}
	b.CacheImports = cacheImports

	b.BuildArgs = filter(opts, buildArgPrefix)
	b.Labels = filter(opts, labelPrefix)
	b.CacheIDNamespace = opts[keyCacheNSArg]
	b.CgroupParent = opts[keyCgroupParent]

	if v, ok := opts[keyHostnameArg]; ok && len(v) > 0 {
		opts[keyHostname] = v
	}
	b.Hostname = opts[keyHostname]
	return nil
}

func (bc *BuildConfig) buildContext(ctx context.Context) (*buildContext, error) {
	bctx, err := bc.g.Do(ctx, "initcontext", func(ctx context.Context) (interface{}, error) {
		if bc.bctx != nil {
			return bc.bctx, nil
		}
		bctx, err := bc.initContext(ctx)
		if err == nil {
			bc.bctx = bctx
		}
		return bctx, err
	})
	if err != nil {
		return nil, err
	}
	return bctx.(*buildContext), nil
}

func (bc *BuildConfig) ReadEntrypoint(ctx context.Context, opts ...llb.LocalOption) (*llb.SourceMap, error) {
	bctx, err := bc.buildContext(ctx)
	if err != nil {
		return nil, err
	}

	var src *llb.State

	if !bctx.forceLocalDockerfile {
		if bctx.dockerfile != nil {
			src = bctx.dockerfile
		} else if bctx.context != nil {
			src = bctx.context
		}
	}

	if src == nil {
		name := "load build definition from " + bctx.filename

		filenames := []string{bctx.filename, bctx.filename + ".dockerignore"}

		// dockerfile is also supported casing moby/moby#10858
		if path.Base(bctx.filename) == DefaultDockerfileName {
			filenames = append(filenames, path.Join(path.Dir(bctx.filename), strings.ToLower(DefaultDockerfileName)))
		}

		opts = append([]llb.LocalOption{
			llb.FollowPaths(filenames),
			llb.SessionID(bc.bopts.SessionID),
			llb.SharedKeyHint(bctx.dockerfileLocalName),
			WithInternalName(name),
			llb.Differ(llb.DiffNone, false),
		}, opts...)

		lsrc := llb.Local(bctx.dockerfileLocalName, opts...)
		src = &lsrc
	}

	def, err := src.Marshal(ctx, bc.marshalOpts()...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to marshal local source")
	}

	defVtx, err := def.Head()
	if err != nil {
		return nil, err
	}

	res, err := bc.client.Solve(ctx, client.SolveRequest{
		Definition: def.ToPB(),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to resolve dockerfile")
	}

	ref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}

	dt, err := ref.ReadFile(ctx, client.ReadRequest{
		Filename: bctx.filename,
	})
	if err != nil {
		if path.Base(bctx.filename) == DefaultDockerfileName {
			var err1 error
			dt, err1 = ref.ReadFile(ctx, client.ReadRequest{
				Filename: path.Join(path.Dir(bctx.filename), strings.ToLower(DefaultDockerfileName)),
			})
			if err1 != nil && err == nil {
				err = err1
			}
		}
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read dockerfile")
		}
	}
	smap := llb.NewSourceMap(src, bctx.filename, dt)
	smap.Definition = def

	dt, err = ref.ReadFile(ctx, client.ReadRequest{
		Filename: bctx.filename + ".dockerignore",
	})
	if err == nil {
		bc.dockerignore = dt
	}
	bc.defaultVertex = defVtx

	return smap, nil
}

func (bc *BuildConfig) MainContext(ctx context.Context, opts ...llb.LocalOption) (*llb.State, error) {
	bctx, err := bc.buildContext(ctx)
	if err != nil {
		return nil, err
	}

	if bctx.context != nil {
		return bctx.context, nil
	}

	if bc.dockerignore == nil {
		st := llb.Local(bctx.contextLocalName,
			llb.SessionID(bc.bopts.SessionID),
			llb.FollowPaths([]string{DefaultDockerignoreName}),
			llb.SharedKeyHint(bctx.contextLocalName+"-"+DefaultDockerignoreName),
			WithInternalName("load "+DefaultDockerignoreName),
			llb.Differ(llb.DiffNone, false),
		)
		def, err := st.Marshal(ctx, bc.marshalOpts()...)
		if err != nil {
			return nil, err
		}
		res, err := bc.client.Solve(ctx, client.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}
		dt, err := ref.ReadFile(ctx, client.ReadRequest{
			Filename: DefaultDockerignoreName,
		})
		if err != nil {
			return nil, err
		}
		if dt == nil {
			dt = []byte{}
		}
		bc.dockerignore = dt
	}

	var excludes []string
	if len(bc.dockerignore) != 0 {
		excludes, err = dockerignore.ReadAll(bytes.NewBuffer(bc.dockerignore))
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse dockerignore")
		}
	}

	opts = append([]llb.LocalOption{
		llb.SessionID(bc.bopts.SessionID),
		llb.ExcludePatterns(excludes),
		llb.SharedKeyHint(bctx.contextLocalName),
		WithInternalName("load build context"),
	}, opts...)

	_ = bctx

	return llb.State{}, errors.Errorf("not implemented")
}

func (bc *BuildConfig) NamedContext(ctx context.Context, name string, opt ContextOpt) (*llb.State, *image.Image, error) {
	return nil, nil, errors.Errorf("not implemented")
}

func (bc *BuildConfig) IsNoCache(name string) bool {
	if len(bc.ignoreCache) == 0 {
		return bc.ignoreCache != nil
	}
	for _, n := range bc.ignoreCache {
		if n == name {
			return true
		}
	}
	return false
}

func WithInternalName(name string) llb.ConstraintsOpt {
	return llb.WithCustomName("[internal] " + name)
}
