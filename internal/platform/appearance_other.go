//go:build !darwin

package platform

import (
	"errors"

	"github.com/Zyko0/go-sdl3/sdl"
)

// DarkWindow is only on macOS.
func DarkWindow(w *sdl.Window, bg uint32) error { return errors.New("not on macOS") }
