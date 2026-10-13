//go:build !darwin

package platform

import (
	"errors"

	"github.com/Zyko0/go-sdl3/sdl"
)

// WindowNumber is only on macOS.
func WindowNumber(w *sdl.Window) (uint32, error) { return 0, errors.New("not on macOS") }
