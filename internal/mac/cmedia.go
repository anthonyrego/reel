package mac

// CoreMedia and CoreVideo: reading the sample buffers ScreenCaptureKit
// delivers. A Sample is only valid during the callback that got it;
// nothing here retains it.

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const coreVideoPath = "/System/Library/Frameworks/CoreVideo.framework/CoreVideo"

// CMTime is CoreMedia's rational time (CMTime.h): value/timescale seconds.
type CMTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

const cmTimeValid = 1 // kCMTimeFlags_Valid

// Valid reports whether t is a real time (not invalid or indefinite).
func (t CMTime) Valid() bool { return t.Flags&cmTimeValid != 0 && t.Timescale != 0 }

// Seconds is t in seconds; NaN when t isn't valid.
func (t CMTime) Seconds() float64 {
	if !t.Valid() {
		return math.NaN()
	}
	return float64(t.Value) / float64(t.Timescale)
}

// CMTimeRange is CoreMedia's CMTimeRange: Duration from Start.
type CMTimeRange struct{ Start, Duration CMTime }

// TimeOf is a valid CMTime of value/timescale seconds.
func TimeOf(value int64, timescale int32) CMTime {
	return CMTime{Value: value, Timescale: timescale, Flags: cmTimeValid}
}

// ASBD is CoreAudio's AudioStreamBasicDescription: what one audio sample
// buffer holds.
type ASBD struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

// The ASBD format flags reel reads (CoreAudioBaseTypes.h).
const (
	audioFlagFloat          = 1 << 0
	audioFlagBigEndian      = 1 << 1
	audioFlagSignedInt      = 1 << 2
	audioFlagNonInterleaved = 1 << 5

	formatLinearPCM = 'l'<<24 | 'p'<<16 | 'c'<<8 | 'm'
)

// Float reports float samples; otherwise they're signed integers.
func (d ASBD) Float() bool { return d.FormatFlags&audioFlagFloat != 0 }

// Interleaved reports one buffer of interleaved channels; otherwise there
// is one buffer per channel.
func (d ASBD) Interleaved() bool { return d.FormatFlags&audioFlagNonInterleaved == 0 }

func (d ASBD) String() string {
	kind := fmt.Sprintf("int%d", d.BitsPerChannel)
	if d.Float() {
		kind = fmt.Sprintf("float%d", d.BitsPerChannel)
	}
	layout := "interleaved"
	if !d.Interleaved() {
		layout = "planar"
	}
	if d.FormatID != formatLinearPCM {
		kind = fourCC(d.FormatID)
	}
	return fmt.Sprintf("%.0f Hz %d ch %s %s", d.SampleRate, d.ChannelsPerFrame, kind, layout)
}

// fourCC spells a four-character code.
func fourCC(c uint32) string {
	return string([]byte{byte(c >> 24), byte(c >> 16), byte(c >> 8), byte(c)})
}

// audioBuffer is CoreAudio's AudioBuffer.
type audioBuffer struct {
	Channels uint32
	Size     uint32
	Data     uintptr
}

// maxAudioBuffers bounds the AudioBufferList reel reads: one buffer per
// channel for planar audio, and microphones have at most a few channels.
const maxAudioBuffers = 16

// audioBufferList is CoreAudio's AudioBufferList with room for
// maxAudioBuffers buffers.
type audioBufferList struct {
	Count   uint32
	_       uint32
	Buffers [maxAudioBuffers]audioBuffer
}

// kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment.
const cmAssure16ByteAlignment = 1 << 0

var cm struct {
	pts, duration      func(sbuf uintptr) CMTime
	numSamples         func(sbuf uintptr) int
	dataReady          func(sbuf uintptr) bool
	imageBuffer        func(sbuf uintptr) uintptr
	attachments        func(sbuf uintptr, create bool) uintptr
	formatDesc         func(sbuf uintptr) uintptr
	streamDesc         func(desc uintptr) *ASBD
	audioBufferList    func(sbuf uintptr, sizeNeeded *uintptr, abl *audioBufferList, ablSize uintptr, structAlloc, blockAlloc uintptr, flags uint32, block *uintptr) int32
	hostClock          func() uintptr
	clockTime          func(clock uintptr) CMTime
	pixelWidth, height func(pb uintptr) uint
	pixelFormat        func(pb uintptr) uint32
	isPlanar           func(pb uintptr) bool
	bytesPerRow        func(pb uintptr) uint
	baseAddress        func(pb uintptr) unsafe.Pointer
	lockBase, unlock   func(pb uintptr, flags uint64) int32
}

// kCVPixelBufferLock_ReadOnly.
const pixelLockReadOnly = 1

// loadCoreMedia resolves the CoreMedia and CoreVideo functions (inside
// loadOnce).
func loadCoreMedia() error {
	media, err := purego.Dlopen(coreMediaPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	video, err := purego.Dlopen(coreVideoPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	for _, f := range []struct {
		fn   any
		lib  uintptr
		name string
	}{
		{&cm.pts, media, "CMSampleBufferGetPresentationTimeStamp"},
		{&cm.duration, media, "CMSampleBufferGetDuration"},
		{&cm.numSamples, media, "CMSampleBufferGetNumSamples"},
		{&cm.dataReady, media, "CMSampleBufferDataIsReady"},
		{&cm.imageBuffer, media, "CMSampleBufferGetImageBuffer"},
		{&cm.attachments, media, "CMSampleBufferGetSampleAttachmentsArray"},
		{&cm.formatDesc, media, "CMSampleBufferGetFormatDescription"},
		{&cm.streamDesc, media, "CMAudioFormatDescriptionGetStreamBasicDescription"},
		{&cm.audioBufferList, media, "CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer"},
		{&cm.hostClock, media, "CMClockGetHostTimeClock"},
		{&cm.clockTime, media, "CMClockGetTime"},
		{&cm.pixelWidth, video, "CVPixelBufferGetWidth"},
		{&cm.height, video, "CVPixelBufferGetHeight"},
		{&cm.pixelFormat, video, "CVPixelBufferGetPixelFormatType"},
		{&cm.isPlanar, video, "CVPixelBufferIsPlanar"},
		{&cm.bytesPerRow, video, "CVPixelBufferGetBytesPerRow"},
		{&cm.baseAddress, video, "CVPixelBufferGetBaseAddress"},
		{&cm.lockBase, video, "CVPixelBufferLockBaseAddress"},
		{&cm.unlock, video, "CVPixelBufferUnlockBaseAddress"},
	} {
		if _, err := purego.Dlsym(f.lib, f.name); err != nil {
			return err
		}
		purego.RegisterLibFunc(f.fn, f.lib, f.name)
	}
	return nil
}

// HostTime is now on the host time clock, which ScreenCaptureKit stamps
// its samples with.
func HostTime() CMTime {
	if load() != nil {
		return CMTime{}
	}
	return cm.clockTime(cm.hostClock())
}

// ClockTime is now on a CMClock.
func ClockTime(clock uintptr) CMTime { return cm.clockTime(clock) }

// Kind is what a sample carries (SCStreamOutputType).
type Kind int

const (
	KindScreen Kind = iota
	KindAudio       // system audio
	KindMic
	// KindCamera is a camera frame (from StartCamera): never an
	// SCStreamOutputType.
	KindCamera
)

func (k Kind) String() string {
	switch k {
	case KindScreen:
		return "screen"
	case KindAudio:
		return "audio"
	case KindMic:
		return "mic"
	case KindCamera:
		return "camera"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Sample is one CMSampleBuffer from the stream. It's only valid during
// the callback.
type Sample struct {
	Kind Kind
	buf  uintptr
}

// Ref is the CMSampleBufferRef.
func (s Sample) Ref() uintptr { return s.buf }

// PTS is the presentation time stamp, on the host time clock.
func (s Sample) PTS() CMTime { return cm.pts(s.buf) }

// Duration is how long the sample lasts (invalid for most video frames).
func (s Sample) Duration() CMTime { return cm.duration(s.buf) }

// Frames is the number of audio frames, or 1 for a video frame.
func (s Sample) Frames() int { return cm.numSamples(s.buf) }

// Ready reports whether the sample's data is ready.
func (s Sample) Ready() bool { return cm.dataReady(s.buf) }

// FrameStatus is a screen sample's SCFrameStatus; ok is false when the
// sample carries none.
func (s Sample) FrameStatus() (st FrameStatus, ok bool) {
	arr := objc.ID(cm.attachments(s.buf, false))
	if arr == 0 || objc.Send[uint](arr, sel("count")) == 0 {
		return 0, false
	}
	dict := arr.Send(sel("objectAtIndex:"), uint(0))
	num := dict.Send(sel("objectForKey:"), constant("SCStreamFrameInfoStatus"))
	if num == 0 {
		return 0, false
	}
	return FrameStatus(objc.Send[int](num, sel("integerValue"))), true
}

// FrameStatus is ScreenCaptureKit's SCFrameStatus (SCStream.h).
type FrameStatus int

const (
	FrameComplete  FrameStatus = 0
	FrameIdle      FrameStatus = 1
	FrameBlank     FrameStatus = 2
	FrameSuspended FrameStatus = 3
	FrameStarted   FrameStatus = 4 // the first frame after the stream starts
	FrameStopped   FrameStatus = 5
)

func (f FrameStatus) String() string {
	switch f {
	case FrameComplete:
		return "complete"
	case FrameIdle:
		return "idle"
	case FrameBlank:
		return "blank"
	case FrameSuspended:
		return "suspended"
	case FrameStarted:
		return "started"
	case FrameStopped:
		return "stopped"
	}
	return fmt.Sprintf("status(%d)", int(f))
}

// Image is a screen sample's pixel buffer size and format; ok is false
// when it has none (idle frames).
func (s Sample) Image() (w, h int, format string, ok bool) {
	pb := cm.imageBuffer(s.buf)
	if pb == 0 {
		return 0, 0, "", false
	}
	return int(cm.pixelWidth(pb)), int(cm.height(pb)), fourCC(cm.pixelFormat(pb)), true
}

// Pixels calls fn with a BGRA frame's pixels (a camera's), w×h, stride
// bytes a row, valid only during fn; it reports false (and doesn't call
// fn) for a frame with no pixels or in another format.
func (s Sample) Pixels(fn func(pix []byte, w, h, stride int)) bool {
	pb := cm.imageBuffer(s.buf)
	if pb == 0 || cm.pixelFormat(pb) != pixelBGRA || cm.isPlanar(pb) {
		return false
	}
	if cm.lockBase(pb, pixelLockReadOnly) != 0 {
		return false
	}
	defer cm.unlock(pb, pixelLockReadOnly)
	w, h, stride := int(cm.pixelWidth(pb)), int(cm.height(pb)), int(cm.bytesPerRow(pb))
	base := cm.baseAddress(pb)
	if base == nil || w <= 0 || h <= 0 || stride < w*4 {
		return false
	}
	fn(unsafe.Slice((*byte)(base), (h-1)*stride+w*4), w, h, stride)
	return true
}

// AudioFormat is an audio sample's format; ok is false for other samples.
func (s Sample) AudioFormat() (ASBD, bool) {
	desc := cm.formatDesc(s.buf)
	if desc == 0 {
		return ASBD{}, false
	}
	d := cm.streamDesc(desc)
	if d == nil {
		return ASBD{}, false
	}
	return *d, true
}

// PCM calls fn with the audio sample's buffers (one per channel when
// planar, one interleaved buffer otherwise), valid only during fn.
func (s Sample) PCM(fn func(format ASBD, bufs [][]byte)) error {
	format, ok := s.AudioFormat()
	if !ok {
		return fmt.Errorf("pcm: not audio")
	}
	// CoreMedia wants the list's exact size (a bigger one fails with
	// kCMSampleBufferError_ArrayTooSmall), so ask for it first.
	var need uintptr
	if st := cm.audioBufferList(s.buf, &need, nil, 0, 0, 0, cmAssure16ByteAlignment, nil); st != 0 {
		return fmt.Errorf("pcm: list size: %d", st)
	}
	var abl audioBufferList
	if need > unsafe.Sizeof(abl) {
		return fmt.Errorf("pcm: %d bytes of buffer list, more than %d channels", need, maxAudioBuffers)
	}
	var block uintptr
	st := cm.audioBufferList(s.buf, nil, &abl, need, 0, 0, cmAssure16ByteAlignment, &block)
	if st != 0 {
		return fmt.Errorf("pcm: CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer: %d", st)
	}
	if block != 0 {
		defer lib.cfRelease(block)
	}
	n := min(int(abl.Count), maxAudioBuffers)
	bufs := make([][]byte, n)
	for i := range n {
		b := abl.Buffers[i]
		if b.Data != 0 && b.Size > 0 {
			bufs[i] = unsafe.Slice((*byte)(ptr(b.Data)), b.Size)
		}
	}
	fn(format, bufs)
	return nil
}

// ptr turns an address CoreMedia gave us into a pointer. As in deref,
// going through &p keeps vet's check satisfied.
func ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// Nanos is t in nanoseconds (0 when it isn't valid). Host times are
// nanoseconds since boot, so this is exact for them; other timescales
// round toward zero without overflowing.
func (t CMTime) Nanos() int64 {
	if !t.Valid() {
		return 0
	}
	if t.Timescale == 1_000_000_000 {
		return t.Value
	}
	s := int64(t.Timescale)
	return t.Value/s*1_000_000_000 + t.Value%s*1_000_000_000/s
}

// Nanos is a valid CMTime of ns nanoseconds.
func Nanos(ns int64) CMTime { return TimeOf(ns, 1_000_000_000) }

// Retain retains a CoreFoundation object (a CMSampleBufferRef) and returns
// it.
func Retain(ref uintptr) uintptr { return lib.cfRetain(ref) }

// Release releases a CoreFoundation object.
func Release(ref uintptr) { lib.cfRelease(ref) }

// WithPool runs fn on a locked OS thread inside an autorelease pool.
func WithPool(fn func()) { withPool(fn) }

// BigEndian reports big-endian samples.
func (d ASBD) BigEndian() bool { return d.FormatFlags&audioFlagBigEndian != 0 }
