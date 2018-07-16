package winlayers

import (
	"archive/tar"
	"context"
	"io"
	"io/ioutil"
	"runtime"
	"strings"

	"github.com/containerd/containerd/archive"
	"github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/mount"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func NewFileSystemApplierWithWindows(cs content.Provider, a diff.Applier) diff.Applier {
	if runtime.GOOS == "windows" {
		return a
	}

	return &winApplier{
		cs: cs,
		a:  a,
	}
}

type winApplier struct {
	cs content.Provider
	a  diff.Applier
}

func (s *winApplier) Apply(ctx context.Context, desc ocispec.Descriptor, mounts []mount.Mount) (d ocispec.Descriptor, err error) {
	if !hasWindowsLayerMode(ctx) {
		return s.a.Apply(ctx, desc, mounts)
	}

	isCompressed, err := images.IsCompressedDiff(ctx, desc.MediaType)
	if err != nil {
		return ocispec.Descriptor{}, errors.Wrapf(errdefs.ErrNotImplemented, "unsupported diff media type: %v", desc.MediaType)
	}

	var ocidesc ocispec.Descriptor
	if err := mount.WithTempMount(ctx, mounts, func(root string) error {
		ra, err := s.cs.ReaderAt(ctx, desc)
		if err != nil {
			return errors.Wrap(err, "failed to get reader from content store")
		}
		defer ra.Close()

		r := content.NewReader(ra)
		if isCompressed {
			ds, err := compression.DecompressStream(r)
			if err != nil {
				return err
			}
			defer ds.Close()
			r = ds
		}

		digester := digest.Canonical.Digester()
		rc := &readCounter{
			r: io.TeeReader(r, digester.Hash()),
		}

		rc2 := filter(rc, func(hdr *tar.Header) bool {
			if strings.HasPrefix(hdr.Name, "Files/") {
				hdr.Name = strings.TrimPrefix(hdr.Name, "Files/")
				hdr.Linkname = strings.TrimPrefix(hdr.Linkname, "Files/")
				return true
			}
			return false
		})

		if _, err := archive.Apply(ctx, root, rc2); err != nil {
			return err
		}

		// Read any trailing data
		if _, err := io.Copy(ioutil.Discard, rc); err != nil {
			return err
		}

		ocidesc = ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayer,
			Size:      rc.c,
			Digest:    digester.Digest(),
		}
		return nil

	}); err != nil {
		return ocispec.Descriptor{}, err
	}
	return ocidesc, nil
}

type readCounter struct {
	r io.Reader
	c int64
}

func (rc *readCounter) Read(p []byte) (n int, err error) {
	n, err = rc.r.Read(p)
	rc.c += int64(n)
	return
}

func filter(in io.Reader, f func(*tar.Header) bool) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		tarReader := tar.NewReader(in)
		tarWriter := tar.NewWriter(pw)

		pw.CloseWithError(func() error {
			for {
				h, err := tarReader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				if f(h) {
					if err := tarWriter.WriteHeader(h); err != nil {
						return err
					}
					if h.Size > 0 {
						if _, err := io.Copy(tarWriter, tarReader); err != nil {
							return err
						}
					}
				} else {
					if h.Size > 0 {
						if _, err := io.Copy(ioutil.Discard, tarReader); err != nil {
							return err
						}
					}
				}
			}
			return tarWriter.Close()
		}())
	}()
	return pr
}
