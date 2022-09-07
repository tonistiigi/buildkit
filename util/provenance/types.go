package provenance

import (
	distreference "github.com/docker/distribution/reference"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type Result struct {
	Ref  *Capture
	Refs map[string]*Capture
}

type ImageSource struct {
	Ref      string
	Platform *ocispecs.Platform
	Digest   digest.Digest
}

type GitSource struct {
	URL    string
	Commit string
}

type HTTPSource struct {
	URL    string
	Digest digest.Digest
}

type LocalSource struct {
	Name string
}

type Secret struct {
	ID       string
	Optional bool
}

type SSH struct {
	ID       string
	Optional bool
}

type Sources struct {
	Images      []ImageSource
	LocalImages []ImageSource
	Git         []GitSource
	HTTP        []HTTPSource
	Local       []LocalSource
}

type Capture struct {
	Frontend            string
	Args                map[string]string
	Sources             Sources
	Secrets             []Secret
	SSH                 []SSH
	NetworkAccess       bool
	IncompleteMaterials bool
}

func parseRefName(s string) (distreference.Named, string, error) {
	ref, err := distreference.ParseNormalizedNamed(s)
	if err != nil {
		return nil, "", err
	}
	name := ref.Name()
	tag := "latest"
	if r, ok := ref.(distreference.Tagged); ok {
		tag = r.Tag()
	}
	return ref, name + ":" + tag, nil
}

// OptimizeImageSources filters out image sources by digest reference if same digest
// is already present by a tag reference.
func (c *Capture) OptimizeImageSources() error {
	m := map[string]struct{}{}
	for _, i := range c.Sources.Images {
		ref, nameTag, err := parseRefName(i.Ref)
		if err != nil {
			return err
		}
		if _, ok := ref.(distreference.Canonical); !ok {
			m[nameTag] = struct{}{}
		}
	}

	images := make([]ImageSource, 0, len(c.Sources.Images))
	for _, i := range c.Sources.Images {
		ref, nameTag, err := parseRefName(i.Ref)
		if err != nil {
			return err
		}
		if _, ok := ref.(distreference.Canonical); ok {
			if _, ok := m[nameTag]; ok {
				continue
			}
		}
		images = append(images, i)
	}
	c.Sources.Images = images
	return nil
}

func (c *Capture) AddImage(i ImageSource) {
	for _, v := range c.Sources.Images {
		if v.Ref == i.Ref {
			if v.Platform == i.Platform {
				return
			}
			if v.Platform != nil && i.Platform != nil {
				if v.Platform.Architecture == i.Platform.Architecture && v.Platform.OS == i.Platform.OS && v.Platform.Variant == i.Platform.Variant {
					return
				}
			}
		}

	}
	c.Sources.Images = append(c.Sources.Images, i)
}

func (c *Capture) AddLocalImage(i ImageSource) {
	for _, v := range c.Sources.LocalImages {
		if v.Ref == i.Ref {
			if v.Platform == i.Platform {
				return
			}
			if v.Platform != nil && i.Platform != nil {
				if v.Platform.Architecture == i.Platform.Architecture && v.Platform.OS == i.Platform.OS && v.Platform.Variant == i.Platform.Variant {
					return
				}
			}
		}

	}
	c.Sources.LocalImages = append(c.Sources.LocalImages, i)
}

func (c *Capture) AddLocal(l LocalSource) {
	for _, v := range c.Sources.Local {
		if v.Name == l.Name {
			return
		}
	}
	c.Sources.Local = append(c.Sources.Local, l)
}

func (c *Capture) AddGit(g GitSource) {
	for _, v := range c.Sources.Git {
		if v.URL == g.URL {
			return
		}
	}
	c.Sources.Git = append(c.Sources.Git, g)
}

func (c *Capture) AddHTTP(h HTTPSource) {
	for _, v := range c.Sources.HTTP {
		if v.URL == h.URL {
			return
		}
	}
	c.Sources.HTTP = append(c.Sources.HTTP, h)
}

func (c *Capture) AddSecret(s Secret) {
	for i, v := range c.Secrets {
		if v.ID == s.ID {
			if !s.Optional {
				c.Secrets[i].Optional = false
			}
			return
		}
	}
	c.Secrets = append(c.Secrets, s)
}

func (c *Capture) AddSSH(s SSH) {
	for i, v := range c.SSH {
		if v.ID == s.ID {
			if !s.Optional {
				c.SSH[i].Optional = false
			}
			return
		}
	}
	c.SSH = append(c.SSH, s)
}
