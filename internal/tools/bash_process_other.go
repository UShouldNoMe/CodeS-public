//go:build !linux && !darwin

package tools

import "os/exec"

// These platforms currently reject isolated Bash before starting a command.
// A future sandbox backend must also implement descendant process cancellation.
func isolateCommandLifetime(*exec.Cmd) func() { return func() {} }
