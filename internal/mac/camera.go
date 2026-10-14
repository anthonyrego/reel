package mac

import (
	"errors"

	"github.com/ebitengine/purego/objc"
)

// ErrNotBundle is a camera asked for outside Reel.app (see InBundle).
var ErrNotBundle = errors.New("only Reel.app can use the camera (make run)")

// Camera is a camera the bubble can show.
type Camera struct {
	// ID is AVCaptureDevice's uniqueID, stable across launches.
	ID      string
	Name    string
	Kind    CameraKind
	Default bool // the system's default camera
}

// CameraKind is what sort of camera it is.
type CameraKind int

const (
	CameraBuiltIn    CameraKind = iota // the Mac's own
	CameraExternal                     // USB and the like
	CameraContinuity                   // an iPhone (Continuity Camera)
)

func (k CameraKind) String() string {
	switch k {
	case CameraBuiltIn:
		return "built-in"
	case CameraContinuity:
		return "iPhone"
	}
	return "external"
}

// The Info.plist keys the camera needs: macOS kills a process that opens a
// camera without the first (no exception, no error: the process is gone),
// and lists iPhones only to apps with the second.
const (
	cameraUsageKey      = "NSCameraUsageDescription"
	continuityCameraKey = "NSCameraUseContinuityCameraDeviceType"
)

// CameraAccess reports the Camera permission without asking.
func CameraAccess() Permission { return access("AVMediaTypeVideo") }

// RequestCameraAccess asks for the Camera permission: macOS shows its
// prompt the first time and answers from the stored choice after that.
// done is called with the answer on an AVFoundation queue. It opens no
// device. Only Reel.app can ask (see InBundle): a binary run from a
// terminal would be asking for the terminal.
func RequestCameraAccess(done func(granted bool)) error {
	if !InBundle() {
		return ErrNotBundle
	}
	return requestAccess("AVMediaTypeVideo", done)
}

// Cameras lists the cameras. Listing doesn't open a device, so it never
// shows the Camera prompt or lights a camera. iPhones are listed only in
// Reel.app, whose Info.plist asks for them.
func Cameras() ([]Camera, error) {
	if err := load(); err != nil {
		return nil, err
	}
	var cams []Camera
	withPool(func() {
		def := ""
		if d := class("AVCaptureDevice").Send(sel("defaultDeviceWithMediaType:"), constant("AVMediaTypeVideo")); d != 0 {
			def = goString(d.Send(sel("uniqueID")))
		}
		kinds := []struct {
			deviceType string
			kind       CameraKind
		}{
			{"AVCaptureDeviceTypeBuiltInWideAngleCamera", CameraBuiltIn},
			{"AVCaptureDeviceTypeExternal", CameraExternal},
		}
		if infoBool(continuityCameraKey) {
			kinds = append(kinds, struct {
				deviceType string
				kind       CameraKind
			}{"AVCaptureDeviceTypeContinuityCamera", CameraContinuity})
		}
		seen := map[string]bool{}
		// One discovery session a type, so each camera's kind is the
		// session that found it.
		for _, k := range kinds {
			session := class("AVCaptureDeviceDiscoverySession").Send(sel("discoverySessionWithDeviceTypes:mediaType:position:"),
				nsArray(constant(k.deviceType)), constant("AVMediaTypeVideo"), 0)
			each(session.Send(sel("devices")), func(d objc.ID) {
				id := goString(d.Send(sel("uniqueID")))
				if seen[id] || !objc.Send[bool](d, sel("isConnected")) {
					return
				}
				seen[id] = true
				cams = append(cams, Camera{ID: id, Name: goString(d.Send(sel("localizedName"))), Kind: k.kind, Default: id == def})
			})
		}
	})
	return cams, nil
}

// InBundle reports whether this process is Reel.app (it has a bundle
// identifier and the camera's usage string), rather than the bare binary
// run from a terminal. Only Reel.app ever opens a camera or asks for it:
// macOS kills a process without the usage string the moment it opens one.
func InBundle() bool {
	if load() != nil {
		return false
	}
	var ok bool
	withPool(func() {
		b := class("NSBundle").Send(sel("mainBundle"))
		ok = b != 0 && goString(b.Send(sel("bundleIdentifier"))) != "" &&
			b.Send(sel("objectForInfoDictionaryKey:"), nsString(cameraUsageKey)) != 0
	})
	return ok
}

// infoBool reads a boolean from the main bundle's Info.plist (false when
// it's missing; inside withPool).
func infoBool(key string) bool {
	b := class("NSBundle").Send(sel("mainBundle"))
	if b == 0 {
		return false
	}
	v := b.Send(sel("objectForInfoDictionaryKey:"), nsString(key))
	return v != 0 && objc.Send[bool](v, sel("boolValue"))
}
