package oci

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/pkg/apparmor"
	cdseccomp "github.com/containerd/containerd/pkg/seccomp"
	"github.com/containerd/continuity/fs"
	"github.com/containerd/log"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/profiles/seccomp"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/entitlements/security"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	selinux "github.com/opencontainers/selinux/go-selinux"
	"github.com/opencontainers/selinux/go-selinux/label"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
	"tags.cncf.io/container-device-interface/pkg/cdi"
	"tags.cncf.io/container-device-interface/pkg/parser"
)

var (
	cgroupNSOnce     sync.Once
	supportsCgroupNS bool
)

const (
	tracingSocketPath = "/dev/otel-grpc.sock"
)

func withProcessArgs(args ...string) oci.SpecOpts {
	return oci.WithProcessArgs(args...)
}

func generateMountOpts(resolvConf, hostsFile string) []oci.SpecOpts {
	return []oci.SpecOpts{
		// https://github.com/moby/buildkit/issues/429
		withRemovedMount("/run"),
		withROBind(resolvConf, "/etc/resolv.conf"),
		withROBind(hostsFile, "/etc/hosts"),
		withCGroup(),
	}
}

// generateSecurityOpts may affect mounts, so must be called after generateMountOpts
func generateSecurityOpts(mode pb.SecurityMode, apparmorProfile string, selinuxB bool) (opts []oci.SpecOpts, _ error) {
	if selinuxB && !selinux.GetEnabled() {
		return nil, errors.New("selinux is not available")
	}
	switch mode {
	case pb.SecurityMode_INSECURE:
		return []oci.SpecOpts{
			security.WithInsecureSpec(),
			oci.WithWriteableCgroupfs,
			oci.WithWriteableSysfs,
			func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
				var err error
				if selinuxB {
					s.Process.SelinuxLabel, s.Linux.MountLabel, err = label.InitLabels([]string{"disable"})
				}
				return err
			},
		}, nil
	case pb.SecurityMode_SANDBOX:
		if cdseccomp.IsEnabled() {
			opts = append(opts, withDefaultProfile())
		}
		if apparmorProfile != "" {
			// If AppArmor is not supported but a profile was specified, return an error
			if !apparmor.HostSupports() {
				return nil, errors.New("AppArmor is not supported on this host, but the profile '" + apparmorProfile + "' was specified")
			}

			opts = append(opts, oci.WithApparmorProfile(apparmorProfile))
		}
		opts = append(opts, func(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
			var err error
			if selinuxB {
				s.Process.SelinuxLabel, s.Linux.MountLabel, err = label.InitLabels(nil)
			}
			return err
		})
		return opts, nil
	}
	return nil, nil
}

// generateProcessModeOpts may affect mounts, so must be called after generateMountOpts
func generateProcessModeOpts(mode ProcessMode) ([]oci.SpecOpts, error) {
	if mode == NoProcessSandbox {
		return []oci.SpecOpts{
			oci.WithHostNamespace(specs.PIDNamespace),
			withBoundProc(),
		}, nil
		// TODO(AkihiroSuda): Configure seccomp to disable ptrace (and prctl?) explicitly
	}
	return nil, nil
}

func generateIDmapOpts(idmap *idtools.IdentityMapping) ([]oci.SpecOpts, error) {
	if idmap == nil {
		return nil, nil
	}
	return []oci.SpecOpts{
		oci.WithUserNamespace(specMapping(idmap.UIDMaps), specMapping(idmap.GIDMaps)),
	}, nil
}

func specMapping(s []idtools.IDMap) []specs.LinuxIDMapping {
	var ids []specs.LinuxIDMapping
	for _, item := range s {
		ids = append(ids, specs.LinuxIDMapping{
			HostID:      uint32(item.HostID),
			ContainerID: uint32(item.ContainerID),
			Size:        uint32(item.Size),
		})
	}
	return ids
}

func generateRlimitOpts(ulimits []*pb.Ulimit) ([]oci.SpecOpts, error) {
	if len(ulimits) == 0 {
		return nil, nil
	}
	var rlimits []specs.POSIXRlimit
	for _, u := range ulimits {
		if u == nil {
			continue
		}
		rlimits = append(rlimits, specs.POSIXRlimit{
			Type: fmt.Sprintf("RLIMIT_%s", strings.ToUpper(u.Name)),
			Hard: uint64(u.Hard),
			Soft: uint64(u.Soft),
		})
	}
	return []oci.SpecOpts{
		func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
			s.Process.Rlimits = rlimits
			return nil
		},
	}, nil
}

// genereateCDIOptions creates the OCI runtime spec options for injecting CDI
// devices. Two options are returned: The first ensures that the CDI registry
// is initialized with refresh disabled, and the second injects the devices
// into the container.
func generateCDIOpts(ctx context.Context, devices []*pb.CDIDevice) ([]oci.SpecOpts, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	var dd []string
	for _, d := range devices {
		if d == nil {
			continue
		}
		if _, _, _, err := parser.ParseQualifiedName(d.Name); err != nil {
			return nil, errors.Wrapf(err, "invalid CDI device name %s", d.Name)
		}
		dd = append(dd, d.Name)
	}

	// withStaticCDIRegistry inits the CDI registry and disables auto-refresh.
	// This is used from the `run` command to avoid creating a registry with
	// auto-refresh enabled. It also provides a way to override the CDI spec
	// file paths if required.
	withStaticCDIRegistry := func() oci.SpecOpts {
		return func(ctx context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
			_ = cdi.Configure(cdi.WithAutoRefresh(false))
			// TODO: this should use worker CDIManager
			if err := cdi.Refresh(); err != nil {
				// We don't consider registry refresh failure a fatal error.
				// For instance, a dynamically generated invalid CDI Spec file
				// for any particular vendor shouldn't prevent injection of
				// devices of different vendors. CDI itself knows better, and
				// it will fail the injection if necessary.
				bklog.G(ctx).Warnf("CDI registry refresh failed: %v", err)
			}
			return nil
		}
	}

	// withCDIDevices injects the requested CDI devices into the OCI specification.
	// FIXME: Use oci.WithCDIDevices once we switch to containerd 2.0.
	withCDIDevices := func(devices ...string) oci.SpecOpts {
		return func(ctx context.Context, _ oci.Client, c *containers.Container, s *specs.Spec) error {
			if len(devices) == 0 {
				return nil
			}
			if err := cdi.Refresh(); err != nil {
				log.G(ctx).Warnf("CDI registry refresh failed: %v", err)
			}
			bklog.G(ctx).Debugf("Injecting CDI devices %v", devices)
			if _, err := cdi.InjectDevices(s, devices...); err != nil {
				return errors.Wrapf(err, "CDI device injection failed")
			}
			// One crucial thing to keep in mind is that CDI device injection
			// might add OCI Spec environment variables, hooks, and mounts as
			// well. Therefore, it is important that none of the corresponding
			// OCI Spec fields are reset up in the call stack once we return.
			return nil
		}
	}

	return []oci.SpecOpts{
		withStaticCDIRegistry(),
		withCDIDevices(dd...),
	}, nil
}

// withDefaultProfile sets the default seccomp profile to the spec.
// Note: must follow the setting of process capabilities
func withDefaultProfile() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		var err error
		s.Linux.Seccomp, err = seccomp.GetDefaultProfile(s)
		return err
	}
}

func withROBind(src, dest string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Mounts = append(s.Mounts, specs.Mount{
			Destination: dest,
			Type:        "bind",
			Source:      src,
			Options:     []string{"nosuid", "noexec", "nodev", "rbind", "ro"},
		})
		return nil
	}
}

func withCGroup() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Mounts = append(s.Mounts, specs.Mount{
			Destination: "/sys/fs/cgroup",
			Type:        "cgroup",
			Source:      "cgroup",
			Options:     []string{"ro", "nosuid", "noexec", "nodev"},
		})
		return nil
	}
}

func withBoundProc() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Mounts = removeMountsWithPrefix(s.Mounts, "/proc")
		procMount := specs.Mount{
			Destination: "/proc",
			Type:        "bind",
			Source:      "/proc",
			// NOTE: "rbind"+"ro" does not make /proc read-only recursively.
			// So we keep maskedPath and readonlyPaths (although not mandatory for rootless mode)
			Options: []string{"rbind"},
		}
		s.Mounts = append([]specs.Mount{procMount}, s.Mounts...)

		var maskedPaths []string
		for _, s := range s.Linux.MaskedPaths {
			if !hasPrefix(s, "/proc") {
				maskedPaths = append(maskedPaths, s)
			}
		}
		s.Linux.MaskedPaths = maskedPaths

		var readonlyPaths []string
		for _, s := range s.Linux.ReadonlyPaths {
			if !hasPrefix(s, "/proc") {
				readonlyPaths = append(readonlyPaths, s)
			}
		}
		s.Linux.ReadonlyPaths = readonlyPaths

		return nil
	}
}

func removeMountsWithPrefix(mounts []specs.Mount, prefixDir string) []specs.Mount {
	var ret []specs.Mount
	for _, m := range mounts {
		if !hasPrefix(m.Destination, prefixDir) {
			ret = append(ret, m)
		}
	}
	return ret
}

func getTracingSocketMount(socket string) *specs.Mount {
	return &specs.Mount{
		Destination: tracingSocketPath,
		Type:        "bind",
		Source:      socket,
		Options:     []string{"ro", "rbind"},
	}
}

func getTracingSocket() string {
	return fmt.Sprintf("unix://%s", tracingSocketPath)
}

func cgroupV2NamespaceSupported() bool {
	// Check if cgroups v2 namespaces are supported.  Trying to do cgroup
	// namespaces with cgroups v1 results in EINVAL when we encounter a
	// non-standard hierarchy.
	// See https://github.com/moby/buildkit/issues/4108
	cgroupNSOnce.Do(func() {
		if _, err := os.Stat("/proc/self/ns/cgroup"); os.IsNotExist(err) {
			return
		}
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.subtree_control"); os.IsNotExist(err) {
			return
		}
		supportsCgroupNS = true
	})
	return supportsCgroupNS
}

func sub(m mount.Mount, subPath string) (mount.Mount, func() error, error) {
	var retries = 10
	root := m.Source
	for {
		src, err := fs.RootPath(root, subPath)
		if err != nil {
			return mount.Mount{}, nil, err
		}
		// similar to runc.WithProcfd
		fh, err := os.OpenFile(src, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return mount.Mount{}, nil, errors.WithStack(err)
		}

		fdPath := "/proc/self/fd/" + strconv.Itoa(int(fh.Fd()))
		if resolved, err := os.Readlink(fdPath); err != nil {
			fh.Close()
			return mount.Mount{}, nil, errors.WithStack(err)
		} else if resolved != src {
			retries--
			if retries <= 0 {
				fh.Close()
				return mount.Mount{}, nil, errors.Errorf("unable to safely resolve subpath %s", subPath)
			}
			fh.Close()
			continue
		}

		m.Source = fdPath
		lm := snapshot.LocalMounterWithMounts([]mount.Mount{m}, snapshot.ForceRemount())
		mp, err := lm.Mount()
		if err != nil {
			fh.Close()
			return mount.Mount{}, nil, err
		}
		m.Source = mp
		fh.Close() // release the fd, we don't need it anymore

		return m, lm.Unmount, nil
	}
}
