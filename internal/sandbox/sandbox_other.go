//go:build !linux && !darwin

package sandbox

// Unsupported platforms deliberately have no OS sandbox implementation. A
// future caller may choose to warn and run normally, ask for permission, or
// disable its shell tool when New returns nil.
func newPlatformSandbox() Sandbox {
	return nil
}
