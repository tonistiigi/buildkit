package staticfs

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"

	"github.com/tonistiigi/fsutil"
	"github.com/tonistiigi/fsutil/types"
)

type File struct {
	Stat types.Stat
	Data []byte
}

type FS struct {
	files map[string]File
}

var _ fsutil.FS = &FS{}

func NewFS() *FS {
	return &FS{
		files: map[string]File{},
	}
}

func (fs *FS) Add(p string, stat types.Stat, data []byte) {
	stat.Size_ = int64(len(data))
	if stat.Mode == 0 {
		stat.Mode = 0644
	}
	stat.Path = p
	fs.files[p] = File{
		Stat: stat,
		Data: data,
	}
}

func (fs *FS) Walk(ctx context.Context, fn filepath.WalkFunc) error {
	keys := make([]string, 0, len(fs.files))
	for k := range fs.files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, p := range keys {
		st := fs.files[p].Stat
		if err := fn(p, &fsutil.StatInfo{Stat: &st}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (fs *FS) Open(p string) (io.ReadCloser, error) {
	if f, ok := fs.files[p]; ok {
		return ioutil.NopCloser(bytes.NewReader(f.Data)), nil
	}
	return nil, os.ErrNotExist
}
