package runcworker

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/sys"
	runc "github.com/containerd/go-runc"
	"github.com/docker/docker/pkg/symlink"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/bridge"
	"github.com/moby/buildkit/worker/oci"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

type runcworker struct {
	runc *runc.Runc
	root string
}

func New(root string) (worker.Worker, error) {
	if err := exec.Command("runc", "--version").Run(); err != nil {
		return nil, errors.Wrap(err, "failed to find runc binary")
	}

	if err := setSubReaper(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", root)
	}

	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// TODO: check that root is not symlink to fail early

	runtime := &runc.Runc{
		Log:          filepath.Join(root, "runc-log.json"),
		LogFormat:    runc.JSON,
		PdeathSignal: syscall.SIGKILL,
		Setpgid:      true,
	}

	w := &runcworker{
		runc: runtime,
		root: root,
	}
	return w, nil
}

func (w *runcworker) Exec(ctx context.Context, meta worker.Meta, root cache.Mountable, mounts []worker.Mount, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error {

	rootMount, err := root.Mount(ctx, false)
	if err != nil {
		return err
	}

	id := identity.NewID()
	bundle := filepath.Join(w.root, id)

	if err := os.Mkdir(bundle, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(bundle)
	rootFSPath := filepath.Join(bundle, "rootfs")
	if err := os.Mkdir(rootFSPath, 0700); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(bundle, "config.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := mount.All(rootMount, rootFSPath); err != nil {
		return err
	}
	defer mount.Unmount(rootFSPath, 0)
	spec.Root.Path = rootFSPath
	if _, ok := root.(cache.ImmutableRef); ok { // TODO: pass in with mount, not ref type
		spec.Root.Readonly = true
	}

	newp, err := symlink.FollowSymlinkInScope(filepath.Join(rootFSPath, meta.Cwd), rootFSPath)
	if err != nil {
		return errors.Wrapf(err, "working dir %s points to invalid target", newp)
	}
	if err := os.MkdirAll(newp, 0700); err != nil {
		return errors.Wrapf(err, "failed to create working directory %s", newp)
	}

	if err := json.NewEncoder(f).Encode(spec); err != nil {
		return err
	}

	forwardIO, err := newForwardIO(stdin, stdout, stderr)
	if err != nil {
		return errors.Wrap(err, "creating new forwarding IO")
	}
	defer forwardIO.Close()

	pidFilePath := filepath.Join(w.root, "runc_pid_"+identity.NewID())
	defer os.RemoveAll(pidFilePath)

	logrus.Debugf("> creating %s %v", id, meta.Args)
	if err := w.runc.Create(ctx, id, bundle, &runc.CreateOpts{
		PidFile: pidFilePath,
		IO:      forwardIO,
	}); err != nil {
		return err
	}
	forwardIO.release()

	defer func() {
		if err := w.runc.Delete(context.TODO(), id, &runc.DeleteOpts{}); err != nil {
			logrus.Errorf("failed to delete %s: %+v", id, err)
		}
	}()

	dt, err := ioutil.ReadFile(pidFilePath)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(dt))
	if err != nil {
		return err
	}

	// FIXME: remove hardcoded "docker0" with user input
	pair, err := bridge.CreateBridgePair("docker0")
	if err != nil {
		return errors.Wrap(err, "error in creating bridge pair")
	}

	if err := pair.Set(pid); err != nil {
		return errors.Wrap(err, "could not set bridge network")
	}
	defer pair.Remove()

	err = w.runc.Start(ctx, id)
	if err != nil {
		return err
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	if ps, err := p.Wait(); err != nil {
		status := 255
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
			status = ws.ExitStatus()
		}
		if status != 0 {
			return errors.Errorf("exit code: %d", status)
		}
	}

	return nil
}

type forwardIO struct {
	stdin, stdout, stderr *os.File
	toRelease             []io.Closer
	toClose               []io.Closer
}

func newForwardIO(stdin io.ReadCloser, stdout, stderr io.WriteCloser) (f *forwardIO, err error) {
	fio := &forwardIO{}
	defer func() {
		if err != nil {
			fio.Close()
		}
	}()
	if stdin != nil {
		fio.stdin, err = fio.readCloserToFile(stdin)
		if err != nil {
			return nil, err
		}
	}
	if stdout != nil {
		fio.stdout, err = fio.writeCloserToFile(stdout)
		if err != nil {
			return nil, err
		}
	}
	if stderr != nil {
		fio.stderr, err = fio.writeCloserToFile(stderr)
		if err != nil {
			return nil, err
		}
	}
	return fio, nil
}

func (s *forwardIO) Close() error {
	s.release()
	var err error
	for _, cl := range s.toClose {
		if err1 := cl.Close(); err == nil {
			err = err1
		}
	}
	s.toClose = nil
	return err
}

// release releases active FDs if the process doesn't need them any more
func (s *forwardIO) release() {
	for _, cl := range s.toRelease {
		cl.Close()
	}
	s.toRelease = nil
}

func (s *forwardIO) Set(cmd *exec.Cmd) {
	cmd.Stdin = s.stdin
	cmd.Stdout = s.stdout
	cmd.Stderr = s.stderr
}

func (s *forwardIO) Stdin() io.WriteCloser {
	return nil
}

func (s *forwardIO) Stdout() io.ReadCloser {
	return nil
}

func (s *forwardIO) Stderr() io.ReadCloser {
	return nil
}

func (s *forwardIO) readCloserToFile(rc io.ReadCloser) (*os.File, error) {
	if f, ok := rc.(*os.File); ok {
		return f, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	s.toClose = append(s.toClose, pw)
	s.toRelease = append(s.toRelease, pr)
	go func() {
		_, err := io.Copy(pw, rc)
		if err1 := pw.Close(); err == nil {
			err = err1
		}
		_ = err
	}()
	return pr, nil
}

func (s *forwardIO) writeCloserToFile(wc io.WriteCloser) (*os.File, error) {
	if f, ok := wc.(*os.File); ok {
		return f, nil
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	s.toClose = append(s.toClose, pr)
	s.toRelease = append(s.toRelease, pw)
	go func() {
		_, err := io.Copy(wc, pr)
		if err1 := pw.Close(); err == nil {
			err = err1
		}
		_ = err
	}()
	return pw, nil
}

var subReaperOnce sync.Once
var subReaperError error

func setSubReaper() error {
	subReaperOnce.Do(func() {
		subReaperError = sys.SetSubreaper(1)
	})
	return subReaperError
}
