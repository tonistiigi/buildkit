package provenance

import (
	"strings"

	"github.com/containerd/containerd/platforms"
	slsa "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	"github.com/moby/buildkit/util/purl"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/package-url/packageurl-go"
)

var BuildKitBuildType = "https://mobyproject.org/buildkit@v1"

type ProvenancePredicate struct {
	slsa.ProvenancePredicate
	Metadata *ProvenanceMetadata                `json:"metadata,omitempty"`
	Source   *Source                            `json:"buildSource,omitempty"`
	Layers   map[string][][]ocispecs.Descriptor `json:"buildLayers,omitempty"`
}

type ProvenanceMetadata struct {
	slsa.ProvenanceMetadata
	VCS map[string]string `json:"vcs,omitempty"`
}

func slsaMaterials(srcs Sources) ([]slsa.ProvenanceMaterial, error) {
	count := len(srcs.Images) + len(srcs.Git) + len(srcs.HTTP) + len(srcs.LocalImages)
	out := make([]slsa.ProvenanceMaterial, 0, count)

	for _, s := range srcs.Images {
		uri, err := purl.RefToPURL(s.Ref, s.Platform)
		if err != nil {
			return nil, err
		}
		out = append(out, slsa.ProvenanceMaterial{
			URI: uri,
			Digest: slsa.DigestSet{
				s.Digest.Algorithm().String(): s.Digest.Hex(),
			},
		})
	}

	for _, s := range srcs.Git {
		out = append(out, slsa.ProvenanceMaterial{
			URI: s.URL,
			Digest: slsa.DigestSet{
				"sha1": s.Commit,
			},
		})
	}

	for _, s := range srcs.HTTP {
		out = append(out, slsa.ProvenanceMaterial{
			URI: s.URL,
			Digest: slsa.DigestSet{
				s.Digest.Algorithm().String(): s.Digest.Hex(),
			},
		})
	}

	for _, s := range srcs.LocalImages {
		q := []packageurl.Qualifier{}
		if s.Platform != nil {
			q = append(q, packageurl.Qualifier{
				Key:   "platform",
				Value: platforms.Format(*s.Platform),
			})
		}
		packageurl.NewPackageURL(packageurl.TypeOCI, "", s.Ref, "", q, "")
		out = append(out, slsa.ProvenanceMaterial{
			URI: s.Ref,
			Digest: slsa.DigestSet{
				s.Digest.Algorithm().String(): s.Digest.Hex(),
			},
		})
	}
	return out, nil
}

func findMaterial(srcs Sources, uri string) (*slsa.ProvenanceMaterial, bool) {
	for _, s := range srcs.Git {
		if s.URL == uri {
			return &slsa.ProvenanceMaterial{
				URI: s.URL,
				Digest: slsa.DigestSet{
					"sha1": s.Commit,
				},
			}, true
		}
	}
	for _, s := range srcs.HTTP {
		if s.URL == uri {
			return &slsa.ProvenanceMaterial{
				URI: s.URL,
				Digest: slsa.DigestSet{
					s.Digest.Algorithm().String(): s.Digest.Hex(),
				},
			}, true
		}
	}
	return nil, false
}

func NewPredicate(c *Capture) (*ProvenancePredicate, error) {
	materials, err := slsaMaterials(c.Sources)
	if err != nil {
		return nil, err
	}
	inv := slsa.ProvenanceInvocation{}

	contextKey := "context"
	if v, ok := c.Args["contextkey"]; ok && v != "" {
		contextKey = v
	}

	if v, ok := c.Args[contextKey]; ok && v != "" {
		if m, ok := findMaterial(c.Sources, v); ok {
			inv.ConfigSource.URI = m.URI
			inv.ConfigSource.Digest = m.Digest
		} else {
			inv.ConfigSource.URI = v
		}
		delete(c.Args, contextKey)
	}

	if v, ok := c.Args["filename"]; ok && v != "" {
		inv.ConfigSource.EntryPoint = v
		delete(c.Args, "filename")
	}

	vcs := make(map[string]string)
	for k, v := range c.Args {
		if strings.HasPrefix(k, "vcs:") {
			delete(c.Args, k)
			if v != "" {
				vcs[strings.TrimPrefix(k, "vcs:")] = v
			}
		}
	}

	inv.Parameters = c.Args

	incompleteMaterials := c.IncompleteMaterials
	if !incompleteMaterials {
		if len(c.Sources.Local) > 0 {
			incompleteMaterials = true
		}
	}

	pr := &ProvenancePredicate{
		ProvenancePredicate: slsa.ProvenancePredicate{
			BuildType:  BuildKitBuildType,
			Invocation: inv,
			Materials:  materials,
		},
		Metadata: &ProvenanceMetadata{
			ProvenanceMetadata: slsa.ProvenanceMetadata{
				Completeness: slsa.ProvenanceComplete{
					Parameters:  true,
					Environment: true,
					Materials:   !incompleteMaterials,
				},
			},
		},
	}

	if len(vcs) > 0 {
		pr.Metadata.VCS = vcs
	}

	return pr, nil
}

func FilterArgs(m map[string]string) map[string]string {
	var hostSpecificArgs = map[string]struct{}{
		"cgroup-parent":      {},
		"image-resolve-mode": {},
		"platform":           {},
	}
	out := make(map[string]string)
	for k, v := range m {
		if _, ok := hostSpecificArgs[k]; ok {
			continue
		}
		if strings.HasPrefix(k, "attest:") {
			continue
		}
		out[k] = v
	}
	return out
}
