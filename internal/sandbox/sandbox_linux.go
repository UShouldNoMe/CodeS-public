//go:build linux

package sandbox

import (
	"fmt"
	"os/exec"
)

const bubblewrapExecutable = "bwrap"

// linuxSandbox uses bubblewrap to create user, PID, mount and optionally
// network namespaces around the command.
type linuxSandbox struct{}

func newPlatformSandbox() Sandbox {
	return &linuxSandbox{}
}

func (s *linuxSandbox) Available() bool {
	_, err := exec.LookPath(bubblewrapExecutable)
	return err == nil
}

func (s *linuxSandbox) Wrap(command string, config Config) (Command, error) {
	bubblewrapPath, err := exec.LookPath(bubblewrapExecutable)
	if err != nil {
		return Command{}, fmt.Errorf("%w: find %s: %v", ErrUnavailable, bubblewrapExecutable, err)
	}

	allowWrite, err := normalizePaths(config.AllowWrite)
	if err != nil {
		return Command{}, fmt.Errorf("normalize writable paths: %w", err)
	}
	denyWrite, err := normalizePaths(config.DenyWrite)
	if err != nil {
		return Command{}, fmt.Errorf("normalize protected paths: %w", err)
	}

	args := []string{
		// The command gets its own user and PID namespaces.
		"--unshare-user",
		"--unshare-pid",

		// Keep terminal ioctls and long-lived descendants away from the host
		// session, and terminate the sandboxed command when its parent dies.
		"--new-session",
		"--die-with-parent",

		// Start with a read-only view of the host filesystem. Later mounts
		// override selected paths with narrower permissions.
		"--ro-bind", "/", "/",
	}

	for _, path := range allowWrite {
		// --bind-try keeps optional teaching/demo paths from making every
		// command fail merely because the path has not been created yet.
		args = append(args, "--bind-try", path, path)
	}
	for _, path := range denyWrite {
		// Deny rules are appended after allow rules so that a protected child
		// path is mounted read-only over a writable parent. Bubblewrap cannot
		// mount a missing source, so the "try" form ignores absent paths.
		args = append(args, "--ro-bind-try", path, path)
	}

	if !config.NetworkEnabled {
		args = append(args, "--unshare-net")
	}

	// Replace the host's /proc and /dev views with sandbox-local mounts.
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--",
		"bash", "-c", command,
	)

	return Command{
		Executable: bubblewrapPath,
		Args:       args,
	}, nil
}
