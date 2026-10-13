//go:build !darwin

package platform

import "errors"

// HotKeyName is the global key OnHotKey registers.
const HotKeyName = "⌃⌥R"

// OnHotKey registers a key that works from any app. Only macOS has one.
func OnHotKey(fn func()) error {
	return errors.New("hot key: not on this platform")
}
