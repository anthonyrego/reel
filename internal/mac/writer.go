package mac

// AVAssetWriter: the MP4 file. One goroutine owns a Writer and calls it
// in order; nothing here locks.
//
// AVAssetWriter raises Objective-C exceptions, which kill the process,
// when it's misused: an input added after writing started, settings it
// can't apply, a sample appended before the session or after the end, a
// second session. The Writer is a state machine that only makes the calls
// its state allows, checks settings with canApplyOutputSettings and
// canAddInput first, and appends only while AVAssetWriter says it's
// writing.

import (
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// AudioSettings is how the audio track is encoded: AAC from the mixer's
// 48 kHz stereo float32.
type AudioSettings struct {
	Rate     int // 48000
	Channels int // 2
	Bitrate  int // bits a second
}

// VideoSettings is how the video track is encoded.
type VideoSettings struct {
	Width, Height int // even
	FPS           int // the expected rate (frames come only on change)
	Bitrate       int // average, bits a second
	// KeyframeEvery is the most time between keyframes: trimming without
	// re-encoding cuts at one.
	KeyframeEvery time.Duration
}

// AVAssetWriterStatus values (AVAssetWriter.h).
const (
	avWriterUnknown   = 0
	avWriterWriting   = 1
	avWriterCompleted = 2
	avWriterFailed    = 3
)

type writerState int

const (
	writerOpen writerState = iota // started writing, session at zero
	writerFinished
	writerFailed
)

// Writer writes one MP4.
type Writer struct {
	path  string
	w     objc.ID // AVAssetWriter, owned
	video objc.ID // AVAssetWriterInput, owned
	audio objc.ID // AVAssetWriterInput, owned; 0 without audio
	pcm   uintptr // the CMAudioFormatDescription of what AppendAudio takes
	state writerState
	err   error

	Appended, Dropped int   // video samples written, and dropped because the encoder was busy
	AudioFrames       int64 // audio frames written
}

// cmSampleTimingInfo is CoreMedia's CMSampleTimingInfo.
type cmSampleTimingInfo struct {
	Duration, PTS, DTS CMTime
}

// The hot path's messages, registered once: objc.Send registers a
// function on every call.
var hot struct {
	ready, appendSB, status objc.SEL
	sendBool                func(id objc.ID, sel objc.SEL) bool
	sendBoolPtr             func(id objc.ID, sel objc.SEL, p uintptr) bool
	sendInt                 func(id objc.ID, sel objc.SEL) int
	copyTiming              func(alloc, sbuf uintptr, n int, timing *cmSampleTimingInfo, out *uintptr) int32
}

// loadWriter resolves what the writer calls (inside loadOnce).
func loadWriter() error {
	libobjc, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&hot.sendBool, libobjc, "objc_msgSend")
	purego.RegisterLibFunc(&hot.sendBoolPtr, libobjc, "objc_msgSend")
	purego.RegisterLibFunc(&hot.sendInt, libobjc, "objc_msgSend")
	media, err := purego.Dlopen(coreMediaPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&hot.copyTiming, media, "CMSampleBufferCreateCopyWithNewTiming")
	hot.ready, hot.appendSB, hot.status = sel("isReadyForMoreMediaData"), sel("appendSampleBuffer:"), sel("status")
	return nil
}

// ErrExists is NewWriter's error when the file is already there: reel
// never writes over a file.
var ErrExists = errors.New("the file already exists")

// NewWriter opens an MP4 at path with an H.264 track and, when audio isn't
// nil, an AAC track, starts writing and starts the session at time zero. fragment, when above zero, writes a
// movie fragment that often, so a recording cut short is still readable
// up to its last fragment.
func NewWriter(path string, v VideoSettings, audio *AudioSettings, fragment time.Duration) (*Writer, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s: %w", path, ErrExists)
	}
	if v.Width <= 0 || v.Height <= 0 || v.Width%2 != 0 || v.Height%2 != 0 {
		return nil, fmt.Errorf("writer: size %d×%d isn't even", v.Width, v.Height)
	}
	wr := &Writer{path: path}
	var err error
	withPool(func() { err = wr.open(v, audio, fragment) })
	if err != nil {
		wr.release()
		os.Remove(path)
		return nil, err
	}
	return wr, nil
}

func (wr *Writer) open(v VideoSettings, a *AudioSettings, fragment time.Duration) error {
	url := class("NSURL").Send(sel("fileURLWithPath:"), nsString(wr.path))
	var nserr objc.ID
	wr.w = class("AVAssetWriter").Send(sel("alloc")).Send(sel("initWithURL:fileType:error:"), url, constant("AVFileTypeMPEG4"), unsafe.Pointer(&nserr))
	if wr.w == 0 {
		return nsError("writer: open "+wr.path, nserr)
	}
	// moov goes first, so a player can start before it has the whole file
	// (Slack, a browser). It costs a rewrite of the file at the end.
	if fragment <= 0 {
		wr.w.Send(sel("setShouldOptimizeForNetworkUse:"), true)
	} else {
		// Network use writes to a temporary file and makes the real one
		// at the end, so a crash would leave nothing: fragments go
		// straight to the file instead.
		wr.w.Send(sel("setMovieFragmentInterval:"), TimeOf(fragment.Milliseconds(), 1000))
	}

	settings := videoSettings(v)
	video := constant("AVMediaTypeVideo")
	if !objc.Send[bool](wr.w, sel("canApplyOutputSettings:forMediaType:"), settings, video) {
		return fmt.Errorf("writer: H.264 can't encode %d×%d at %d bits a second", v.Width, v.Height, v.Bitrate)
	}
	wr.video = class("AVAssetWriterInput").Send(sel("alloc")).Send(sel("initWithMediaType:outputSettings:"), video, settings)
	if wr.video == 0 {
		return errors.New("writer: no video input")
	}
	// Frames come as they're captured: an input that waited for the
	// encoder would back up into ScreenCaptureKit's pool.
	wr.video.Send(sel("setExpectsMediaDataInRealTime:"), true)
	if !objc.Send[bool](wr.w, sel("canAddInput:"), wr.video) {
		return errors.New("writer: can't add the video input")
	}
	wr.w.Send(sel("addInput:"), wr.video)
	if a != nil {
		if err := wr.openAudio(*a); err != nil {
			return err
		}
	}

	if !objc.Send[bool](wr.w, sel("startWriting")) {
		return nsError("writer: start", wr.w.Send(sel("error")))
	}
	wr.w.Send(sel("startSessionAtSourceTime:"), TimeOf(0, 1))
	wr.state = writerOpen
	return nil
}

// videoSettings is the H.264 track's outputSettings (autoreleased).
func videoSettings(v VideoSettings) objc.ID {
	keyframe := v.KeyframeEvery
	if keyframe <= 0 {
		keyframe = 2 * time.Second
	}
	compression := nsDict(
		constant("AVVideoAverageBitRateKey"), nsInt(v.Bitrate),
		constant("AVVideoMaxKeyFrameIntervalDurationKey"), nsFloat(keyframe.Seconds()),
		// No B-frames: the encoder holds no frames back, so it never
		// keeps more of ScreenCaptureKit's surfaces than it must.
		constant("AVVideoAllowFrameReorderingKey"), nsBool(false),
		constant("AVVideoProfileLevelKey"), constant("AVVideoProfileLevelH264HighAutoLevel"),
		constant("AVVideoExpectedSourceFrameRateKey"), nsInt(v.FPS),
	)
	// Tagged BT.709, as the frames are converted (ScreenCaptureKit gives
	// sRGB, the same primaries, through the 709 matrix): players that
	// guess for untagged video guess differently.
	color := nsDict(
		constant("AVVideoColorPrimariesKey"), constant("AVVideoColorPrimaries_ITU_R_709_2"),
		constant("AVVideoTransferFunctionKey"), constant("AVVideoTransferFunction_ITU_R_709_2"),
		constant("AVVideoYCbCrMatrixKey"), constant("AVVideoYCbCrMatrix_ITU_R_709_2"),
	)
	return nsDict(
		constant("AVVideoCodecKey"), constant("AVVideoCodecTypeH264"),
		constant("AVVideoWidthKey"), nsInt(v.Width),
		constant("AVVideoHeightKey"), nsInt(v.Height),
		constant("AVVideoCompressionPropertiesKey"), compression,
		constant("AVVideoColorPropertiesKey"), color,
	)
}

// AppendVideo writes a video frame (a CMSampleBufferRef from the stream)
// at recording time pts, lasting dur (invalid: until the next frame). It
// reports whether the frame was written; a frame the encoder has no room
// for is dropped and counted. Inside withPool, on the writer's goroutine.
func (wr *Writer) AppendVideo(sbuf uintptr, pts, dur CMTime) (bool, error) {
	if wr.state != writerOpen {
		return false, wr.failure()
	}
	if hot.sendInt(wr.w, hot.status) != avWriterWriting {
		return false, wr.fail(nsError("writer", wr.w.Send(sel("error"))))
	}
	if !hot.sendBool(wr.video, hot.ready) {
		wr.Dropped++
		return false, nil
	}
	timing := cmSampleTimingInfo{Duration: dur, PTS: pts}
	var cp uintptr
	if st := hot.copyTiming(0, sbuf, 1, &timing, &cp); st != 0 || cp == 0 {
		return false, wr.fail(fmt.Errorf("writer: retime a frame: %d", st))
	}
	ok := hot.sendBoolPtr(wr.video, hot.appendSB, cp)
	lib.cfRelease(cp) // the input retains what it keeps
	if !ok {
		return false, wr.fail(nsError("writer: append a frame", wr.w.Send(sel("error"))))
	}
	wr.Appended++
	return true, nil
}

// Finish ends the session at end (samples after it are cut), finishes
// the file and waits for it. The Writer is done after, whatever happens.
func (wr *Writer) Finish(end CMTime) error {
	defer wr.release()
	if wr.state != writerOpen {
		return wr.failure()
	}
	var err error
	withPool(func() {
		wr.w.Send(sel("endSessionAtSourceTime:"), end)
		wr.video.Send(sel("markAsFinished"))
		if wr.audio != 0 {
			wr.audio.Send(sel("markAsFinished"))
		}
		done := make(chan struct{})
		block := objc.NewBlock(func(objc.Block) { close(done) })
		defer block.Release()
		wr.w.Send(sel("finishWritingWithCompletionHandler:"), block)
		select {
		case <-done:
		case <-time.After(60 * time.Second): // network-use rewrites the whole file
			err = fmt.Errorf("writer: finish: %w", errTimeout)
			return
		}
		if st := objc.Send[int](wr.w, sel("status")); st != avWriterCompleted {
			err = nsError("writer: finish", wr.w.Send(sel("error")))
		}
	})
	if err != nil {
		wr.state = writerFailed
		return err
	}
	wr.state = writerFinished
	return nil
}

// Cancel stops writing and deletes the file.
func (wr *Writer) Cancel() {
	if wr.state == writerOpen {
		withPool(func() { wr.w.Send(sel("cancelWriting")) })
		os.Remove(wr.path)
	}
	wr.state = writerFailed
	wr.release()
}

func (wr *Writer) fail(err error) error {
	wr.state, wr.err = writerFailed, err
	return err
}

func (wr *Writer) failure() error {
	if wr.err != nil {
		return wr.err
	}
	return errors.New("writer: not writing")
}

func (wr *Writer) release() {
	if wr.pcm != 0 {
		lib.cfRelease(wr.pcm)
		wr.pcm = 0
	}
	if wr.audio != 0 {
		wr.audio.Send(sel("release"))
		wr.audio = 0
	}
	if wr.video != 0 {
		wr.video.Send(sel("release"))
		wr.video = 0
	}
	if wr.w != 0 {
		wr.w.Send(sel("release"))
		wr.w = 0
	}
}

// nsDict makes an autoreleased NSDictionary from key, value pairs.
func nsDict(kv ...objc.ID) objc.ID {
	n := len(kv) / 2
	keys, vals := make([]objc.ID, n), make([]objc.ID, n)
	for i := range n {
		keys[i], vals[i] = kv[2*i], kv[2*i+1]
		if keys[i] == 0 || vals[i] == 0 {
			// A nil in a dictionary raises an exception.
			panic(fmt.Sprintf("mac: nil in a dictionary at pair %d", i))
		}
	}
	return class("NSDictionary").Send(sel("dictionaryWithObjects:forKeys:count:"),
		unsafe.Pointer(&vals[0]), unsafe.Pointer(&keys[0]), uint(n))
}

func nsInt(v int) objc.ID       { return class("NSNumber").Send(sel("numberWithLongLong:"), int64(v)) }
func nsFloat(v float64) objc.ID { return class("NSNumber").Send(sel("numberWithDouble:"), v) }
func nsBool(v bool) objc.ID     { return class("NSNumber").Send(sel("numberWithBool:"), v) }

// kAudioFormatMPEG4AAC and the LPCM flags (CoreAudioBaseTypes.h).
const (
	formatAAC         = 'a'<<24 | 'a'<<16 | 'c'<<8 | ' '
	audioFlagPacked   = 1 << 3
	blockAssureMemory = 1 << 0 // kCMBlockBufferAssureMemoryNowFlag
)

// The audio path's CoreMedia functions.
var cmAudio struct {
	formatCreate func(alloc uintptr, asbd *ASBD, layoutSize, layout, cookieSize, cookie, ext uintptr, out *uintptr) int32
	blockCreate  func(structAlloc, mem, length, blockAlloc, custom, offset, dataLength uintptr, flags uint32, out *uintptr) int32
	blockReplace func(src unsafe.Pointer, dst, offset, length uintptr) int32
	sampleCreate func(alloc, block, format uintptr, frames int, pts CMTime, packets uintptr, out *uintptr) int32
}

// loadAudioWriter resolves them (inside loadOnce).
func loadAudioWriter() error {
	media, err := purego.Dlopen(coreMediaPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&cmAudio.formatCreate, media, "CMAudioFormatDescriptionCreate")
	purego.RegisterLibFunc(&cmAudio.blockCreate, media, "CMBlockBufferCreateWithMemoryBlock")
	purego.RegisterLibFunc(&cmAudio.blockReplace, media, "CMBlockBufferReplaceDataBytes")
	purego.RegisterLibFunc(&cmAudio.sampleCreate, media, "CMAudioSampleBufferCreateReadyWithPacketDescriptions")
	return nil
}

// openAudio adds the AAC input and makes the format description of the
// PCM AppendAudio takes (inside withPool, before startWriting).
func (wr *Writer) openAudio(a AudioSettings) error {
	settings := nsDict(
		constant("AVFormatIDKey"), nsInt(formatAAC),
		constant("AVSampleRateKey"), nsFloat(float64(a.Rate)),
		constant("AVNumberOfChannelsKey"), nsInt(a.Channels),
		constant("AVEncoderBitRateKey"), nsInt(a.Bitrate),
	)
	kind := constant("AVMediaTypeAudio")
	if !objc.Send[bool](wr.w, sel("canApplyOutputSettings:forMediaType:"), settings, kind) {
		return fmt.Errorf("writer: AAC can't encode %d Hz × %d at %d bits a second", a.Rate, a.Channels, a.Bitrate)
	}
	wr.audio = class("AVAssetWriterInput").Send(sel("alloc")).Send(sel("initWithMediaType:outputSettings:"), kind, settings)
	if wr.audio == 0 {
		return errors.New("writer: no audio input")
	}
	wr.audio.Send(sel("setExpectsMediaDataInRealTime:"), true)
	if !objc.Send[bool](wr.w, sel("canAddInput:"), wr.audio) {
		return errors.New("writer: can't add the audio input")
	}
	wr.w.Send(sel("addInput:"), wr.audio)

	asbd := ASBD{
		SampleRate: float64(a.Rate), FormatID: formatLinearPCM,
		FormatFlags:    audioFlagFloat | audioFlagPacked,
		BytesPerPacket: uint32(4 * a.Channels), FramesPerPacket: 1, BytesPerFrame: uint32(4 * a.Channels),
		ChannelsPerFrame: uint32(a.Channels), BitsPerChannel: 32,
	}
	if st := cmAudio.formatCreate(0, &asbd, 0, 0, 0, 0, 0, &wr.pcm); st != 0 {
		return fmt.Errorf("writer: audio format: %d", st)
	}
	return nil
}

// AppendAudio writes interleaved float32 PCM (in the AudioSettings'
// channels) starting at frame start of the recording. It reports false,
// writing nothing, when the encoder has no room yet: the caller keeps the
// block and tries again. The PCM is copied. Inside withPool, on the
// writer's goroutine.
func (wr *Writer) AppendAudio(pcm []float32, channels int, rate int, start int64) (bool, error) {
	if wr.state != writerOpen || wr.audio == 0 {
		return false, wr.failure()
	}
	frames := len(pcm) / channels
	if frames == 0 {
		return true, nil
	}
	if hot.sendInt(wr.w, hot.status) != avWriterWriting {
		return false, wr.fail(nsError("writer", wr.w.Send(sel("error"))))
	}
	if !hot.sendBool(wr.audio, hot.ready) {
		return false, nil
	}
	size := uintptr(frames * channels * 4)
	// CoreMedia allocates and owns the memory; the samples are copied in.
	// (Go memory handed to it would be freed by its allocator.)
	var block uintptr
	if st := cmAudio.blockCreate(0, 0, size, 0, 0, 0, size, blockAssureMemory, &block); st != 0 {
		return false, wr.fail(fmt.Errorf("writer: audio block: %d", st))
	}
	defer lib.cfRelease(block)
	if st := cmAudio.blockReplace(unsafe.Pointer(&pcm[0]), block, 0, size); st != 0 {
		return false, wr.fail(fmt.Errorf("writer: audio copy: %d", st))
	}
	var sbuf uintptr
	if st := cmAudio.sampleCreate(0, block, wr.pcm, frames, TimeOf(start, int32(rate)), 0, &sbuf); st != 0 {
		return false, wr.fail(fmt.Errorf("writer: audio sample: %d", st))
	}
	ok := hot.sendBoolPtr(wr.audio, hot.appendSB, sbuf)
	lib.cfRelease(sbuf)
	if !ok {
		return false, wr.fail(nsError("writer: append audio", wr.w.Send(sel("error"))))
	}
	wr.AudioFrames += int64(frames)
	return true, nil
}
