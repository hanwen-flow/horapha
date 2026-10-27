package nsrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/engflow/horapha/internal/bazel"
)

const OuterChildArg = "__horapha_outer_namespace__"

// OuterRun keeps the experiment controller and its clients in one private mount
// namespace, outside the nested PID namespaces used for each JVM restore.
func OuterRun(ctx context.Context, argv []string) error {
	if len(argv) < 2 || argv[0] != "--" {
		return fmt.Errorf("namespace: expected -- COMMAND [ARGS...]")
	}
	if os.Getuid() == 0 || os.Geteuid() != os.Getuid() || os.Getegid() != os.Getgid() {
		return fmt.Errorf("namespace: requires a non-root user with matching real and effective IDs")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, self, append([]string{OuterChildArg}, argv[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{capSysAdmin},
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 45 * time.Second
	if err := cmd.Run(); err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			return &bazel.ExitError{Code: exited.ExitCode()}
		}
		return err
	}
	return nil
}

func OuterChild(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("namespace: missing command")
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("namespace: make-rprivate: %w", err)
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(bin, argv, os.Environ())
}
