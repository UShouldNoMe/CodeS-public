//go:build darwin

package sandbox

import (
	"fmt"
	"os"
	"strings"
)

// Use an absolute path so a modified PATH cannot substitute another program
// for Apple's sandbox launcher.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// darwinSandbox uses a dynamically generated Seatbelt profile.
type darwinSandbox struct{}

func newPlatformSandbox() Sandbox {
	return &darwinSandbox{}
}

func (s *darwinSandbox) Available() bool {
	info, err := os.Stat(sandboxExecPath)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func (s *darwinSandbox) Wrap(command string, config Config) (Command, error) {
	if !s.Available() {
		return Command{}, fmt.Errorf("%w: %s", ErrUnavailable, sandboxExecPath)
	}

	profile, err := buildSeatbeltProfile(config)
	if err != nil {
		return Command{}, err
	}

	// The profile and command are separate argv entries. They are not embedded
	// in an outer "bash -c" string, so $, quotes and command substitutions in
	// command cannot execute before sandbox-exec establishes the sandbox.
	return Command{
		Executable: sandboxExecPath,
		Args: []string{
			"-p", profile,
			"bash", "-c", command,
		},
	}, nil
}

func buildSeatbeltProfile(config Config) (string, error) {
	allowWrite, err := normalizePaths(config.AllowWrite)
	if err != nil {
		return "", fmt.Errorf("normalize writable paths: %w", err)
	}
	denyWrite, err := normalizePaths(config.DenyWrite)
	if err != nil {
		return "", fmt.Errorf("normalize protected paths: %w", err)
	}

	var profile strings.Builder
	profile.WriteString("(version 1)\n")
	profile.WriteString("(deny default)\n")
	profile.WriteString("(allow process-exec)\n")
	profile.WriteString("(allow process-fork)\n")
	profile.WriteString("(allow sysctl-read)\n")

	// Like the Linux backend's read-only root bind, this teaching policy lets
	// development tools read the host filesystem while limiting modifications.
	profile.WriteString("(allow file-read* (subpath \"/\"))\n")

	for _, path := range allowWrite {
		fmt.Fprintf(&profile, "(allow file-write* (subpath %q))\n", path)
	}

	for _, path := range denyWrite {
		// Existing directories need a subpath rule to protect their contents.
		// Files and paths that do not exist yet use an exact literal rule.
		info, statErr := os.Stat(path)
		if statErr == nil && info.IsDir() {
			fmt.Fprintf(&profile, "(deny file-write* (subpath %q))\n", path)
			continue
		}
		fmt.Fprintf(&profile, "(deny file-write* (literal %q))\n", path)
	}

	if config.NetworkEnabled {
		profile.WriteString("(allow network*)\n")
	} else {
		profile.WriteString("(deny network*)\n")
	}

	return profile.String(), nil
}
