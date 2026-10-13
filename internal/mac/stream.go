package mac

// ScreenCaptureKit streams: one SCStream carrying the screen, system audio
// and a microphone, delivered to a Go handler.
//
// Samples arrive on one serial dispatch queue of ours, so the handler never
// runs twice at once and never on a real-time audio thread. The queue
// drains an autorelease pool after every sample. The handler gets each
// sample only for the duration of the call: it reads what it needs, or
// retains the buffer itself, and returns quickly.

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Target is what a stream records.
type Target struct {
	// DisplayID records a whole display (or SourceRect of it), without
	// reel's own windows.
	DisplayID uint32
	// WindowID records one window, wherever it is and whatever covers it.
	// It wins over DisplayID.
	WindowID uint32
	// OwnAppOnly records only this app on the display: its windows and its
	// sound (self-tests, which must not hear or see anything else).
	OwnAppOnly bool
}

// StreamConfig is a stream's setup.
type StreamConfig struct {
	Target Target
	// Width and Height are the frames' size in pixels; ScreenCaptureKit
	// scales the content to fit.
	Width, Height int
	FPS           int    // the most frames a second (frames come only when the content changes)
	PixelFormat   string // a four-character code: "420v" (default) or "BGRA"
	ShowsCursor   bool
	// SourceRect, when not empty, is the part of the display to record,
	// in the display's own points.
	SourceRect Rect

	SystemAudio bool
	// IncludeOwnAudio records reel's own sounds too (self-tests).
	IncludeOwnAudio bool
	// Mic is the microphone to record: an AVCaptureDevice uniqueID, or
	// MicDefault, or "" for none.
	Mic string
}

// MicDefault records the system's default microphone.
const MicDefault = "default"

// Handler gets every sample, on the stream's queue.
type Handler func(Sample)

// Stream is a running SCStream.
type Stream struct {
	stream, output objc.ID
	handler        Handler

	mu      sync.Mutex
	stopped bool
	onStop  func(error)
}

// streams finds the Stream an output object belongs to.
var streams = struct {
	sync.Mutex
	m map[objc.ID]*Stream
}{m: map[objc.ID]*Stream{}}

var output struct {
	once  sync.Once
	class objc.Class
	err   error
	queue uintptr
}

// The dispatch functions and constants (dispatch/queue.h).
var dispatch struct {
	attrAutorelease func(attr uintptr, frequency uint) uintptr
	create          func(label *byte, attr uintptr) uintptr
}

const dispatchAutoreleaseWorkItem = 1 // DISPATCH_AUTORELEASE_FREQUENCY_WORK_ITEM

// queueLabel lives as long as the process; libdispatch keeps a pointer.
var queueLabel = []byte("com.anthonyrego.reel.capture\x00")

// loadOutput registers the output class and creates the samples' queue,
// once.
func loadOutput() error {
	output.once.Do(func() {
		p, err := purego.Dlsym(purego.RTLD_DEFAULT, "dispatch_queue_attr_make_with_autorelease_frequency")
		if err != nil {
			output.err = err
			return
		}
		purego.RegisterFunc(&dispatch.attrAutorelease, p)
		if p, err = purego.Dlsym(purego.RTLD_DEFAULT, "dispatch_queue_create"); err != nil {
			output.err = err
			return
		}
		purego.RegisterFunc(&dispatch.create, p)
		output.queue = dispatch.create(&queueLabel[0], dispatch.attrAutorelease(0, dispatchAutoreleaseWorkItem))
		if output.queue == 0 {
			output.err = errors.New("no dispatch queue")
			return
		}

		protos := []*objc.Protocol{
			protocol("SCStreamOutput", "stream:didOutputSampleBuffer:ofType:", "v@:@^vq"),
			protocol("SCStreamDelegate", "stream:didStopWithError:", "v@:@@"),
		}
		output.class, output.err = objc.RegisterClass("ReelStreamOutput", objc.GetClass("NSObject"), protos, nil, []objc.MethodDef{
			{Cmd: sel("stream:didOutputSampleBuffer:ofType:"), Fn: didOutput},
			{Cmd: sel("stream:didStopWithError:"), Fn: didStop},
		})
	})
	return output.err
}

// didOutput is SCStreamOutput's stream:didOutputSampleBuffer:ofType:, on
// the samples' queue.
func didOutput(self objc.ID, _ objc.SEL, _ objc.ID, sbuf uintptr, kind int) {
	streams.Lock()
	s := streams.m[self]
	streams.Unlock()
	if s == nil || sbuf == 0 {
		return
	}
	s.handler(Sample{Kind: Kind(kind), buf: sbuf})
}

// didStop is SCStreamDelegate's stream:didStopWithError:: the stream
// stopped by itself (the menu bar's stop control, sleep, the window
// closed).
func didStop(self objc.ID, _ objc.SEL, _ objc.ID, nserr objc.ID) {
	streams.Lock()
	s := streams.m[self]
	streams.Unlock()
	if s == nil {
		return
	}
	err := nsError("stream stopped", nserr)
	slog.Warn("mac: stream stopped by itself", "err", err)
	s.finish(err)
}

// StartStream starts recording. handler gets every sample on the stream's
// queue; onStop (which may be nil) hears once when the stream ends, with
// the error if it stopped by itself, or nil after Stop.
func StartStream(cfg StreamConfig, handler Handler, onStop func(error)) (*Stream, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if err := loadOutput(); err != nil {
		return nil, fmt.Errorf("stream: %w", err)
	}
	if !lib.preflight() {
		return nil, ErrNoScreenAccess
	}
	s := &Stream{handler: handler, onStop: onStop}
	var err error
	withPool(func() { err = s.start(cfg) })
	if err != nil {
		s.release()
		return nil, err
	}
	return s, nil
}

func (s *Stream) start(cfg StreamConfig) error {
	content, err := shareableContent()
	if err != nil {
		return err
	}
	defer content.Send(sel("release"))
	filter, err := contentFilter(content, cfg.Target)
	if err != nil {
		return err
	}
	conf, err := streamConfiguration(cfg)
	if err != nil {
		return err
	}

	s.output = objc.ID(output.class).Send(sel("alloc")).Send(sel("init"))
	streams.Lock()
	streams.m[s.output] = s
	streams.Unlock()
	s.stream = class("SCStream").Send(sel("alloc")).Send(sel("initWithFilter:configuration:delegate:"), filter, conf, s.output)
	if s.stream == 0 {
		return errors.New("stream: SCStream wouldn't start with this setup")
	}
	kinds := []Kind{KindScreen}
	if cfg.SystemAudio {
		kinds = append(kinds, KindAudio)
	}
	if cfg.Mic != "" {
		kinds = append(kinds, KindMic)
	}
	for _, k := range kinds {
		var nserr objc.ID
		if !objc.Send[bool](s.stream, sel("addStreamOutput:type:sampleHandlerQueue:error:"), s.output, int(k), output.queue, unsafe.Pointer(&nserr)) {
			return nsError("stream: add "+k.String()+" output", nserr)
		}
	}
	return waitHandler("stream: start", func(done objc.Block) {
		s.stream.Send(sel("startCaptureWithCompletionHandler:"), done)
	})
}

// Stop stops recording and waits for ScreenCaptureKit to say so. Samples
// already queued may still reach the handler until it returns.
func (s *Stream) Stop() error {
	var err error
	withPool(func() {
		err = waitHandler("stream: stop", func(done objc.Block) {
			s.stream.Send(sel("stopCaptureWithCompletionHandler:"), done)
		})
	})
	s.finish(nil)
	return err
}

// SyncClock is the clock the stream's samples are stamped against (0 if
// it has none).
func (s *Stream) SyncClock() uintptr {
	return uintptr(s.stream.Send(sel("synchronizationClock")))
}

// finish tells onStop once and lets go of the stream.
func (s *Stream) finish(err error) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	onStop := s.onStop
	s.mu.Unlock()
	if onStop != nil {
		onStop(err)
	}
	s.release()
}

func (s *Stream) release() {
	if s.output != 0 {
		streams.Lock()
		delete(streams.m, s.output)
		streams.Unlock()
	}
	// The SCStream and its output object are leaked deliberately: the
	// stream may still be finishing on its queues, and a stream is a few
	// objects per recording.
}

// waitHandler calls send with a completion block taking an NSError and
// waits for it.
func waitHandler(what string, send func(done objc.Block)) error {
	got := make(chan error, 1)
	done := objc.NewBlock(func(_ objc.Block, nserr objc.ID) {
		if nserr != 0 {
			got <- nsError(what, nserr)
			return
		}
		got <- nil
	})
	defer done.Release()
	send(done)
	select {
	case err := <-got:
		return err
	case <-time.After(callTimeout):
		return fmt.Errorf("%s: %w", what, errTimeout)
	}
}

// contentFilter is the filter for a target (autoreleased, inside
// withPool).
func contentFilter(content objc.ID, t Target) (objc.ID, error) {
	if t.WindowID != 0 {
		var win objc.ID
		each(content.Send(sel("windows")), func(w objc.ID) {
			if win == 0 && objc.Send[uint32](w, sel("windowID")) == t.WindowID {
				win = w
			}
		})
		if win == 0 {
			return 0, fmt.Errorf("stream: no window %d", t.WindowID)
		}
		return class("SCContentFilter").Send(sel("alloc")).Send(sel("initWithDesktopIndependentWindow:"), win).Send(sel("autorelease")), nil
	}
	var display objc.ID
	each(content.Send(sel("displays")), func(d objc.ID) {
		if display == 0 && (t.DisplayID == 0 || objc.Send[uint32](d, sel("displayID")) == t.DisplayID) {
			display = d
		}
	})
	if display == 0 {
		return 0, fmt.Errorf("stream: no display %d", t.DisplayID)
	}
	self := ownApp(content.Send(sel("applications")))
	if t.OwnAppOnly {
		if self == 0 {
			return 0, errors.New("stream: ScreenCaptureKit doesn't list reel (it lists only apps with a window)")
		}
		return class("SCContentFilter").Send(sel("alloc")).Send(sel("initWithDisplay:includingApplications:exceptingWindows:"),
			display, nsArray(self), nsArray()).Send(sel("autorelease")), nil
	}
	exclude := nsArray()
	if self != 0 {
		exclude = nsArray(self)
	}
	return class("SCContentFilter").Send(sel("alloc")).Send(sel("initWithDisplay:excludingApplications:exceptingWindows:"),
		display, exclude, nsArray()).Send(sel("autorelease")), nil
}

// ownApp is this process among ScreenCaptureKit's applications.
func ownApp(apps objc.ID) objc.ID {
	pid := int32(os.Getpid())
	var self objc.ID
	each(apps, func(a objc.ID) {
		if self == 0 && objc.Send[int32](a, sel("processID")) == pid {
			self = a
		}
	})
	return self
}

// streamConfiguration builds the SCStreamConfiguration (autoreleased,
// inside withPool).
func streamConfiguration(cfg StreamConfig) (objc.ID, error) {
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, fmt.Errorf("stream: size %d×%d", cfg.Width, cfg.Height)
	}
	fps := cfg.FPS
	if fps <= 0 {
		fps = 30
	}
	format := cfg.PixelFormat
	if format == "" {
		format = "420v"
	}
	if len(format) != 4 {
		return 0, fmt.Errorf("stream: pixel format %q", format)
	}
	c := class("SCStreamConfiguration").Send(sel("alloc")).Send(sel("init")).Send(sel("autorelease"))
	c.Send(sel("setWidth:"), uint(cfg.Width))
	c.Send(sel("setHeight:"), uint(cfg.Height))
	c.Send(sel("setMinimumFrameInterval:"), TimeOf(1, int32(fps)))
	c.Send(sel("setPixelFormat:"), uint32(format[0])<<24|uint32(format[1])<<16|uint32(format[2])<<8|uint32(format[3]))
	// The most ScreenCaptureKit allows: the writer keeps the last frame
	// and the encoder holds a few while it works.
	c.Send(sel("setQueueDepth:"), 8)
	// sRGB through the BT.709 matrix, as the writer tags the file.
	c.Send(sel("setColorSpaceName:"), constant("kCGColorSpaceSRGB"))
	c.Send(sel("setColorMatrix:"), constant("kCGDisplayStreamYCbCrMatrix_ITU_R_709_2"))
	c.Send(sel("setShowsCursor:"), cfg.ShowsCursor)
	if r := cfg.SourceRect; r.W > 0 && r.H > 0 {
		c.Send(sel("setSourceRect:"), cgRect{r.X, r.Y, r.W, r.H})
	}
	if cfg.SystemAudio {
		c.Send(sel("setCapturesAudio:"), true)
		c.Send(sel("setSampleRate:"), 48000)
		c.Send(sel("setChannelCount:"), 2)
		c.Send(sel("setExcludesCurrentProcessAudio:"), !cfg.IncludeOwnAudio)
	}
	if cfg.Mic != "" {
		c.Send(sel("setCaptureMicrophone:"), true)
		if cfg.Mic != MicDefault {
			c.Send(sel("setMicrophoneCaptureDeviceID:"), nsString(cfg.Mic))
		}
	}
	return c, nil
}

// protocol is the runtime's protocol of that name. ScreenCaptureKit
// declares its protocols only in its headers: their runtime objects exist
// once some loaded binary refers to one (a Swift app that conforms does),
// so reel registers them itself when they're missing, with the one
// optional method it implements. Nothing in ScreenCaptureKit can be
// checking conformance against an object it never created; registering
// them keeps conformsToProtocol: truthful for anyone who asks by name.
func protocol(name, method, types string) *objc.Protocol {
	if p := objc.GetProtocol(name); p != nil {
		return p
	}
	p := objc.AllocateProtocol(name)
	if p == nil { // registered meanwhile
		return objc.GetProtocol(name)
	}
	p.AddMethodDescription(sel(method), types, false, true)
	p.Register()
	return p
}
