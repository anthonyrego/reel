package mac

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ebitengine/purego/objc"
)

// Rect is a rectangle in global display points, y down from the top of
// the main display (CoreGraphics' and SDL's convention).
type Rect struct{ X, Y, W, H float64 }

// Display is a display the recording can take.
type Display struct {
	ID    uint32 // CGDirectDisplayID
	Frame Rect
	// Scale is pixels per point (2 on a Retina panel or a HiDPI mode).
	Scale float64
}

// Window is a window the recording can take.
type Window struct {
	ID       uint32 // CGWindowID
	Title    string
	App      string
	BundleID string
	PID      int32
	Frame    Rect
	OnScreen bool
}

// ErrNoScreenAccess is the error when Screen Recording isn't granted.
var ErrNoScreenAccess = errors.New("no Screen Recording permission: allow reel in System Settings → Privacy & Security → Screen & System Audio Recording, then start it again")

// Content is what can be recorded now: the displays, and the normal
// windows of other apps (on-screen ones first), never reel's own.
type Content struct {
	Displays []Display
	Windows  []Window
}

// ShareableContent lists what can be recorded. It needs Screen Recording
// and doesn't ask for it.
func ShareableContent() (Content, error) {
	if err := load(); err != nil {
		return Content{}, err
	}
	if !lib.preflight() {
		return Content{}, ErrNoScreenAccess
	}
	var out Content
	var err error
	withPool(func() {
		var content objc.ID
		if content, err = shareableContent(); err != nil {
			return
		}
		defer content.Send(sel("release"))
		each(content.Send(sel("displays")), func(d objc.ID) {
			out.Displays = append(out.Displays, display(d))
		})
		pid := int32(os.Getpid())
		var on, off []Window
		each(content.Send(sel("windows")), func(w objc.ID) {
			win, ok := window(w)
			if !ok || win.PID == pid {
				return
			}
			if win.OnScreen {
				on = append(on, win)
			} else {
				off = append(off, win)
			}
		})
		out.Windows = append(on, off...)
	})
	return out, err
}

// shareableContent is what ScreenCaptureKit can capture now (every window,
// on screen or not), retained. Inside withPool.
func shareableContent() (objc.ID, error) {
	type result struct {
		content objc.ID
		err     error
	}
	got := make(chan result, 1)
	done := objc.NewBlock(func(_ objc.Block, content, nserr objc.ID) {
		if content == 0 {
			got <- result{err: nsError("shareable content", nserr)}
			return
		}
		got <- result{content: content.Send(sel("retain"))}
	})
	defer done.Release()
	class("SCShareableContent").Send(sel("getShareableContentExcludingDesktopWindows:onScreenWindowsOnly:completionHandler:"), true, false, done)
	select {
	case r := <-got:
		return r.content, r.err
	case <-time.After(callTimeout):
		return 0, fmt.Errorf("shareable content: %w", errTimeout)
	}
}

// display reads an SCDisplay. The pixel scale comes from a content filter
// over it, as ScreenCaptureKit itself would capture it.
func display(d objc.ID) Display {
	f := objc.Send[cgRect](d, sel("frame"))
	out := Display{
		ID:    objc.Send[uint32](d, sel("displayID")),
		Frame: Rect{f.X, f.Y, f.W, f.H},
		Scale: 1,
	}
	filter := class("SCContentFilter").Send(sel("alloc")).Send(sel("initWithDisplay:excludingApplications:exceptingWindows:"),
		d, nsArray(), nsArray())
	if filter != 0 {
		if s := objc.Send[float32](filter, sel("pointPixelScale")); s > 0 {
			out.Scale = float64(s)
		}
		filter.Send(sel("release"))
	}
	return out
}

// minWindowEdge leaves out slivers: status items, invisible helpers.
const minWindowEdge = 64

// window reads an SCWindow, and says whether it's one a person would
// record: a normal window (layer 0) of a named app, big enough to see.
func window(w objc.ID) (Window, bool) {
	if objc.Send[int](w, sel("windowLayer")) != 0 {
		return Window{}, false
	}
	f := objc.Send[cgRect](w, sel("frame"))
	if f.W < minWindowEdge || f.H < minWindowEdge {
		return Window{}, false
	}
	app := w.Send(sel("owningApplication"))
	if app == 0 {
		return Window{}, false
	}
	out := Window{
		ID:       objc.Send[uint32](w, sel("windowID")),
		Title:    goString(w.Send(sel("title"))),
		App:      goString(app.Send(sel("applicationName"))),
		BundleID: goString(app.Send(sel("bundleIdentifier"))),
		PID:      objc.Send[int32](app, sel("processID")),
		Frame:    Rect{f.X, f.Y, f.W, f.H},
		OnScreen: objc.Send[bool](w, sel("isOnScreen")),
	}
	return out, out.App != ""
}
