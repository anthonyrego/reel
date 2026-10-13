package platform

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// pointerProperty is SDL_GetPointerProperty, which go-sdl3 doesn't wrap.
//
// Copied from an earlier project of the author's.
var pointerProperty struct {
	once sync.Once
	fn   func(props sdl.PropertiesID, name string, def uintptr) uintptr
	err  error
}

func loadPointerProperty() error {
	pointerProperty.once.Do(func() {
		LoadSDL()
		fn, err := purego.Dlsym(purego.RTLD_DEFAULT, "SDL_GetPointerProperty")
		if err != nil {
			pointerProperty.err = fmt.Errorf("SDL_GetPointerProperty: %w", err)
			return
		}
		purego.RegisterFunc(&pointerProperty.fn, fn)
	})
	return pointerProperty.err
}

// nsWindow is the window's NSWindow.
func nsWindow(w *sdl.Window) (objc.ID, error) {
	if err := loadPointerProperty(); err != nil {
		return 0, err
	}
	props, err := w.Properties()
	if err != nil {
		return 0, err
	}
	p := pointerProperty.fn(props, "SDL.window.cocoa.window", 0)
	if p == 0 {
		return 0, errors.New("the window has no NSWindow")
	}
	return objc.ID(p), nil
}

// WindowNumber is the window's CGWindowID: how ScreenCaptureKit names it.
// Main thread.
func WindowNumber(w *sdl.Window) (uint32, error) {
	nw, err := nsWindow(w)
	if err != nil {
		return 0, err
	}
	n := objc.Send[int](nw, objc.RegisterName("windowNumber"))
	if n <= 0 {
		return 0, errors.New("the window isn't on screen yet")
	}
	return uint32(n), nil
}

// WindowState describes how AppKit has a window, for logs: whether it's
// visible (occlusionState's visible bit), on the active Space, and its
// window level.
func WindowState(w *sdl.Window) string {
	nw, err := nsWindow(w)
	if err != nil {
		return err.Error()
	}
	occl := objc.Send[uint](nw, objc.RegisterName("occlusionState"))
	return fmt.Sprintf("visible=%v occlusionVisible=%v activeSpace=%v level=%d key=%v",
		objc.Send[bool](nw, objc.RegisterName("isVisible")), occl&(1<<1) != 0,
		objc.Send[bool](nw, objc.RegisterName("isOnActiveSpace")), objc.Send[int](nw, objc.RegisterName("level")),
		objc.Send[bool](nw, objc.RegisterName("isKeyWindow")))
}

// nsSize is AppKit's NSSize.
type nsSize struct{ W, H float64 }

// nsRect is AppKit's NSRect.
type nsRect struct {
	X, Y float64
	Size nsSize
}

// WindowFrameSize is the window's whole size in points, title bar
// included: what ScreenCaptureKit records of it. Main thread.
func WindowFrameSize(w *sdl.Window) (float64, float64, error) {
	nw, err := nsWindow(w)
	if err != nil {
		return 0, 0, err
	}
	f := objc.Send[nsRect](nw, objc.RegisterName("frame"))
	return f.Size.W, f.Size.H, nil
}
