package platform

// Floating windows: reel's controls while recording (a see-through pill)
// and the area selection (a full-display picture above the menu bar).
//
// Copied from an earlier project of the author's (MakeOverlay, which was see-through and click-through), with
// both made choices.

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Zyko0/go-sdl3/sdl"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Window levels (NSWindowLevel).
const (
	// LevelFloating is above ordinary windows (NSFloatingWindowLevel).
	LevelFloating = 3
	// LevelOverMenuBar is above the Dock (20) and the menu bar (24)
	// (NSStatusWindowLevel).
	LevelOverMenuBar = 25
)

// NSWindowCollectionBehavior bits.
const (
	behaviorCanJoinAllSpaces    = 1 << 0
	behaviorStationary          = 1 << 4 // Mission Control leaves it in place
	behaviorIgnoresCycle        = 1 << 6 // ⌘` skips it
	behaviorFullScreenAuxiliary = 1 << 8
)

// floatingSelectors are every selector Float sends, by class (checked
// against AppKit by the tests: an unknown one raises an exception that
// kills the process).
var floatingSelectors = map[string][]string{
	"NSWindow": {"setIgnoresMouseEvents:", "setHasShadow:", "setCollectionBehavior:", "setOpaque:",
		"setBackgroundColor:", "setLevel:", "contentView", "windowNumber", "frame",
		"isVisible", "occlusionState", "isOnActiveSpace", "level", "isKeyWindow"},
	"NSView":   {"viewWithTag:", "layer"},
	"SDL3View": {"updateLayer", "layer"},
	"CALayer":  {"setBackgroundColor:", "setOpaque:"},
	"+NSColor": {"clearColor"},
}

// Floating is how Float leaves a window.
type Floating struct {
	// SeeThrough makes it transparent wherever a frame is cleared to alpha
	// 0 (the window draws its own shape), with no shadow.
	SeeThrough bool
	// Level is its window level (LevelFloating, LevelOverMenuBar).
	Level int
}

// Float makes a window made with gfx.WindowConfig.Overlay, once the GPU has
// claimed it, float: at its level on every Space, out of the ⌘` cycle,
// and see-through when asked. It keeps the mouse. Call it on the main
// thread before each time the window is shown: it is cheap and settles
// anything AppKit changed since.
//
// SDL makes a window see-through only for SDL_WINDOW_TRANSPARENT, which its
// GPU API refuses to claim, so this does what SDL would: the window and its
// Metal layer non-opaque, a clear window background, and no background on
// SDL's content view. That view paints its background black each time
// AppKit redisplays it (-[SDL3View updateLayer]), so it becomes a subclass
// whose updateLayer runs SDL's and then clears it again.
func Float(w *sdl.Window, f Floating) error {
	win, err := nsWindow(w)
	if err != nil {
		return err
	}
	if f.SeeThrough {
		content := win.Send(sel("contentView"))
		metal, err := metalView(w, content)
		if err != nil {
			return err
		}
		if err := clearContentView(content); err != nil {
			return err
		}
		metal.Send(sel("layer")).Send(sel("setOpaque:"), false)
		clear := objc.ID(objc.GetClass("NSColor")).Send(sel("clearColor"))
		win.Send(sel("setOpaque:"), false)
		win.Send(sel("setBackgroundColor:"), clear)
		win.Send(sel("setHasShadow:"), false)
	}
	win.Send(sel("setIgnoresMouseEvents:"), false)
	win.Send(sel("setLevel:"), f.Level)
	win.Send(sel("setCollectionBehavior:"),
		uint(behaviorCanJoinAllSpaces|behaviorStationary|behaviorIgnoresCycle|behaviorFullScreenAuxiliary))
	return nil
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

var clearView struct {
	once     sync.Once
	class    objc.Class
	setClass func(obj objc.ID, c objc.Class) objc.Class
	err      error
}

// clearContentView makes SDL's content view a ReelClearContentView (once)
// and clears its background.
func clearContentView(content objc.ID) error {
	clearView.once.Do(func() {
		fn, err := purego.Dlsym(purego.RTLD_DEFAULT, "object_setClass")
		if err != nil {
			clearView.err = fmt.Errorf("float: %w", err)
			return
		}
		purego.RegisterFunc(&clearView.setClass, fn)
		updateLayer, setBackground, layer := sel("updateLayer"), sel("setBackgroundColor:"), sel("layer")
		clearView.class, clearView.err = objc.RegisterClass("ReelClearContentView", content.Class(), nil, nil, []objc.MethodDef{{
			Cmd: updateLayer,
			Fn: func(self objc.ID, cmd objc.SEL) {
				self.SendSuper(updateLayer)
				self.Send(layer).Send(setBackground, uintptr(0))
			},
		}})
	})
	if clearView.err != nil {
		return clearView.err
	}
	if content.Class() != clearView.class {
		clearView.setClass(content, clearView.class)
	}
	content.Send(sel("layer")).Send(sel("setBackgroundColor:"), uintptr(0))
	return nil
}

// metalView is the view SDL's GPU API draws into: a subview of the content
// view, found by the tag SDL gives it.
func metalView(w *sdl.Window, content objc.ID) (objc.ID, error) {
	props, err := w.Properties()
	if err != nil {
		return 0, err
	}
	tag := props.NumberProperty("SDL.window.cocoa.metal_view_tag", 0)
	if tag == 0 || content == 0 {
		return 0, errors.New("float: no Metal view (claim the window first)")
	}
	v := content.Send(sel("viewWithTag:"), int(tag))
	if v == 0 {
		return 0, errors.New("float: no Metal view")
	}
	return v, nil
}
