package mac

import (
	"errors"

	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// CopyFile puts the file at path on the clipboard, as Finder's Copy does:
// pasting into Slack (or Mail, or Finder) pastes the file.
func CopyFile(path string) error {
	if err := load(); err != nil {
		return err
	}
	var ok bool
	withPool(func() {
		url := class("NSURL").Send(sel("fileURLWithPath:"), nsString(path))
		pb := class("NSPasteboard").Send(sel("generalPasteboard"))
		pb.Send(sel("clearContents"))
		ok = objc.Send[bool](pb, sel("writeObjects:"), nsArray(url))
	})
	if !ok {
		return errors.New("the clipboard wouldn't take the file")
	}
	return nil
}

// Trash moves the file at path to the Trash.
func Trash(path string) error {
	if err := load(); err != nil {
		return err
	}
	var err error
	withPool(func() {
		url := class("NSURL").Send(sel("fileURLWithPath:"), nsString(path))
		var nserr objc.ID
		fm := class("NSFileManager").Send(sel("defaultManager"))
		if !objc.Send[bool](fm, sel("trashItemAtURL:resultingItemURL:error:"), url, uintptr(0), unsafe.Pointer(&nserr)) {
			err = nsError("move to the Trash", nserr)
		}
	})
	return err
}

// DisplayNames are the displays' names ("Studio Display", "Built-in Retina
// Display") by CGDirectDisplayID. Main thread.
func DisplayNames() map[uint32]string {
	out := map[uint32]string{}
	if load() != nil {
		return out
	}
	withPool(func() {
		key := nsString("NSScreenNumber")
		each(class("NSScreen").Send(sel("screens")), func(s objc.ID) {
			num := s.Send(sel("deviceDescription")).Send(sel("objectForKey:"), key)
			if num == 0 {
				return
			}
			out[objc.Send[uint32](num, sel("unsignedIntValue"))] = goString(s.Send(sel("localizedName")))
		})
	})
	return out
}

// kCGWindowListOptionIncludingWindow (CGWindow.h).
const cgWindowListIncludingWindow = 1 << 3

var cgWindows struct {
	copyInfo func(option uint32, id uint32) uintptr
}

func loadWindows() error {
	cg, err := purego.Dlopen(coreGraphicsPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&cgWindows.copyInfo, cg, "CGWindowListCopyWindowInfo")
	return nil
}

// WindowOnScreen reports whether window id is on screen, and whether it
// exists at all. A recording of a window that hid, minimized or closed
// holds a still picture (ScreenCaptureKit says nothing on its stream), so
// the app watches.
func WindowOnScreen(id uint32) (onScreen, exists bool) {
	if load() != nil {
		return false, false
	}
	withPool(func() {
		list := objc.ID(cgWindows.copyInfo(cgWindowListIncludingWindow, id))
		if list == 0 {
			return
		}
		defer lib.cfRelease(uintptr(list))
		each(list, func(d objc.ID) {
			if objc.Send[uint32](d.Send(sel("objectForKey:"), constant("kCGWindowNumber")), sel("unsignedIntValue")) != id {
				return
			}
			exists = true
			if v := d.Send(sel("objectForKey:"), constant("kCGWindowIsOnscreen")); v != 0 {
				onScreen = objc.Send[bool](v, sel("boolValue"))
			}
		})
	})
	return onScreen, exists
}
