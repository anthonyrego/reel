package mac

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"unsafe"

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

// ErrNoCameraAccess is a camera opened without the Camera permission:
// opening one while macOS hasn't asked yet would show its prompt, and
// prompts come only from the app's buttons.
var ErrNoCameraAccess = errors.New("reel doesn't have the Camera permission")

// pixelBGRA is kCVPixelFormatType_32BGRA, what the bubble reads.
const pixelBGRA = 'B'<<24 | 'G'<<16 | 'R'<<8 | 'A'

// CameraStream is a running camera. Its frames go to a Go handler on the
// camera's own serial queue (not the screen's), each a Sample of
// KindCamera valid only during the call.
type CameraStream struct {
	session, delegate objc.ID
	observers         []objc.ID
	handler           func(Sample)

	mu      sync.Mutex
	stopped bool
	onStop  func(error)
}

// cameras finds the CameraStream a delegate object belongs to: a frame
// from a stopped camera, still queued, finds none and is dropped.
var cameras = struct {
	sync.Mutex
	m map[objc.ID]*CameraStream
}{m: map[objc.ID]*CameraStream{}}

var cameraOutput struct {
	once  sync.Once
	class objc.Class
	err   error
	queue uintptr
}

// cameraQueueLabel lives as long as the process; libdispatch keeps a
// pointer.
var cameraQueueLabel = []byte("com.anthonyrego.reel.camera\x00")

// loadCamera registers the camera's delegate class and creates its queue,
// once.
func loadCamera() error {
	cameraOutput.once.Do(func() {
		if cameraOutput.queue, cameraOutput.err = newQueue(cameraQueueLabel); cameraOutput.err != nil {
			return
		}
		protos := []*objc.Protocol{protocol("AVCaptureVideoDataOutputSampleBufferDelegate",
			[2]string{"captureOutput:didOutputSampleBuffer:fromConnection:", "v@:@^v@"})}
		cameraOutput.class, cameraOutput.err = objc.RegisterClass("ReelCameraOutput", objc.GetClass("NSObject"), protos, nil, []objc.MethodDef{
			{Cmd: sel("captureOutput:didOutputSampleBuffer:fromConnection:"), Fn: cameraDidOutput},
		})
	})
	return cameraOutput.err
}

// cameraDidOutput is AVCaptureVideoDataOutputSampleBufferDelegate's
// captureOutput:didOutputSampleBuffer:fromConnection:, on the camera's
// queue.
func cameraDidOutput(self objc.ID, _ objc.SEL, _ objc.ID, sbuf uintptr, _ objc.ID) {
	cameras.Lock()
	c := cameras.m[self]
	cameras.Unlock()
	if c == nil || sbuf == 0 {
		return
	}
	c.handler(Sample{Kind: KindCamera, buf: sbuf})
}

// StartCamera opens a camera (an ID from Cameras) and starts its frames,
// 1280×720 when it can, BGRA. It blocks until the camera runs (about a
// second): call it off the main thread. onStop (which may be nil) hears
// once when the camera stops by itself (unplugged, or taken by the
// system), with why.
//
// It refuses without the Camera permission (opening a camera that hasn't
// been asked about shows macOS's prompt) and outside Reel.app (macOS kills
// a process without the usage string the moment it opens one): an agent's
// bare binary never lights the camera.
func StartCamera(id string, handler func(Sample), onStop func(error)) (*CameraStream, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if err := loadCamera(); err != nil {
		return nil, fmt.Errorf("camera: %w", err)
	}
	if !InBundle() {
		return nil, ErrNotBundle
	}
	if CameraAccess() != PermissionGranted {
		return nil, ErrNoCameraAccess
	}
	c := &CameraStream{handler: handler, onStop: onStop}
	var err error
	withPool(func() { err = c.start(id) })
	if err != nil {
		c.release()
		return nil, err
	}
	return c, nil
}

func (c *CameraStream) start(id string) error {
	device := class("AVCaptureDevice").Send(sel("deviceWithUniqueID:"), nsString(id))
	if device == 0 {
		return fmt.Errorf("camera: no camera %s (unplugged?)", id)
	}
	var nserr objc.ID
	input := class("AVCaptureDeviceInput").Send(sel("deviceInputWithDevice:error:"), device, unsafe.Pointer(&nserr))
	if input == 0 {
		return nsError("camera: open", nserr)
	}
	c.session = class("AVCaptureSession").Send(sel("alloc")).Send(sel("init"))
	c.session.Send(sel("beginConfiguration"))
	if preset := constant("AVCaptureSessionPreset1280x720"); objc.Send[bool](c.session, sel("canSetSessionPreset:"), preset) {
		c.session.Send(sel("setSessionPreset:"), preset)
	}
	if !objc.Send[bool](c.session, sel("canAddInput:"), input) {
		c.session.Send(sel("commitConfiguration"))
		return errors.New("camera: the session won't take the camera")
	}
	c.session.Send(sel("addInput:"), input)

	out := class("AVCaptureVideoDataOutput").Send(sel("alloc")).Send(sel("init")).Send(sel("autorelease"))
	bgra := false
	each(out.Send(sel("availableVideoCVPixelFormatTypes")), func(n objc.ID) {
		bgra = bgra || objc.Send[uint32](n, sel("unsignedIntValue")) == pixelBGRA
	})
	if !bgra {
		c.session.Send(sel("commitConfiguration"))
		return errors.New("camera: the camera's output has no BGRA")
	}
	// Setting a format the output doesn't offer would throw: checked
	// above. The picture is mirrored in Go: the connection's mirroring
	// throws unless its automatic adjustment is turned off first.
	out.Send(sel("setVideoSettings:"), nsDict(constant("kCVPixelBufferPixelFormatTypeKey"),
		class("NSNumber").Send(sel("numberWithLongLong:"), int64(pixelBGRA))))
	out.Send(sel("setAlwaysDiscardsLateVideoFrames:"), true)
	c.delegate = objc.ID(cameraOutput.class).Send(sel("alloc")).Send(sel("init"))
	cameras.Lock()
	cameras.m[c.delegate] = c
	cameras.Unlock()
	out.Send(sel("setSampleBufferDelegate:queue:"), c.delegate, cameraOutput.queue)
	if !objc.Send[bool](c.session, sel("canAddOutput:"), out) {
		c.session.Send(sel("commitConfiguration"))
		return errors.New("camera: the session won't take the output")
	}
	c.session.Send(sel("addOutput:"), out)
	c.session.Send(sel("commitConfiguration"))

	// Unplugged, or a runtime error (another app took the camera
	// exclusively, the system reclaimed it): the camera has stopped.
	center := class("NSNotificationCenter").Send(sel("defaultCenter"))
	for _, w := range []struct {
		name   string
		object objc.ID
	}{{"AVCaptureDeviceWasDisconnectedNotification", device}, {"AVCaptureSessionRuntimeErrorNotification", c.session}} {
		name := w.name
		block := objc.NewBlock(func(_ objc.Block, note objc.ID) {
			err := errors.New("camera: unplugged")
			if name == "AVCaptureSessionRuntimeErrorNotification" {
				err = nsError("camera: stopped", note.Send(sel("userInfo")).Send(sel("objectForKey:"), constant("AVCaptureSessionErrorKey")))
			}
			slog.Warn("mac: camera stopped by itself", "err", err)
			// Not on the notification's thread, which may be the
			// session's own: stopping it from there could wait on itself.
			go c.finish(err)
		})
		obs := center.Send(sel("addObserverForName:object:queue:usingBlock:"), constant(name), w.object, objc.ID(0), block)
		c.observers = append(c.observers, obs.Send(sel("retain")))
	}

	c.session.Send(sel("startRunning"))
	if !objc.Send[bool](c.session, sel("isRunning")) {
		return errors.New("camera: it wouldn't start")
	}
	return nil
}

// Stop stops the camera (blocking, like StartCamera). Frames already
// queued are dropped.
func (c *CameraStream) Stop() {
	c.mu.Lock()
	c.onStop = nil
	c.mu.Unlock()
	c.finish(nil)
}

// finish tells onStop once and lets the camera go.
func (c *CameraStream) finish(err error) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	onStop := c.onStop
	c.mu.Unlock()
	c.release()
	if onStop != nil {
		onStop(err)
	}
}

func (c *CameraStream) release() {
	if c.delegate != 0 {
		cameras.Lock()
		delete(cameras.m, c.delegate)
		cameras.Unlock()
	}
	withPool(func() {
		center := class("NSNotificationCenter").Send(sel("defaultCenter"))
		for _, o := range c.observers {
			center.Send(sel("removeObserver:"), o)
			o.Send(sel("release"))
		}
		c.observers = nil
		if c.session != 0 {
			if objc.Send[bool](c.session, sel("isRunning")) {
				c.session.Send(sel("stopRunning"))
			}
			c.session.Send(sel("release"))
			c.session = 0
		}
	})
	// The observers' blocks may be running (one may be why we're here):
	// they're a few bytes per camera opened, never released. So is the
	// delegate object, which the output may still hold while it drains its
	// queue.
}
