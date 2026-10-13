//go:build !darwin

package platform

import (
	"errors"

	"github.com/Zyko0/go-sdl3/sdl"
)

const (
	LevelFloating    = 3
	LevelOverMenuBar = 25
)

// Floating is how Float leaves a window.
type Floating struct {
	SeeThrough bool
	Level      int
}

// Float is only on macOS.
func Float(w *sdl.Window, f Floating) error { return errors.New("not on macOS") }
