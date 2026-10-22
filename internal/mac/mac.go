// Package mac is reel's bridge to the macOS frameworks it records with:
// ScreenCaptureKit, AVFoundation, CoreMedia, CoreGraphics and
// CoreFoundation, called through purego (no cgo). It never imports SDL.
//
// Calling into Objective-C has one sharp edge: an unknown selector, a nil
// in an NSDictionary or a misused AVAssetWriter raises an exception, and
// nothing in Go can catch it, so the process dies. Every selector, symbol
// and constant this package uses is listed in the tables below and checked
// by the tests without touching a device, and callers keep the framework
// objects in states where they can't throw.
//
// Work that needs an autorelease pool runs inside withPool, on a locked OS
// thread. Completion handlers arrive on the frameworks' queues and hand
// their results to a channel.
package mac

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const (
	screenCaptureKitPath = "/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit"
	avFoundationPath     = "/System/Library/Frameworks/AVFoundation.framework/AVFoundation"
	avfAudioPath         = "/System/Library/Frameworks/AVFAudio.framework/AVFAudio"
	coreMediaPath        = "/System/Library/Frameworks/CoreMedia.framework/CoreMedia"
	coreGraphicsPath     = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	coreFoundationPath   = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	foundationPath       = "/System/Library/Frameworks/Foundation.framework/Foundation"

	// callTimeout bounds a wait for a completion handler.
	callTimeout = 5 * time.Second
)

// selectors are every selector the package sends, by class (checked by the
// tests). Class methods are listed under "+Class".
var selectors = map[string][]string{
	"+SCShareableContent":  {"getShareableContentExcludingDesktopWindows:onScreenWindowsOnly:completionHandler:"},
	"SCShareableContent":   {"displays", "windows", "applications"},
	"SCDisplay":            {"displayID", "width", "height", "frame"},
	"SCWindow":             {"windowID", "frame", "title", "windowLayer", "owningApplication", "isOnScreen"},
	"SCRunningApplication": {"bundleIdentifier", "applicationName", "processID"},
	"SCContentFilter": {"initWithDisplay:excludingApplications:exceptingWindows:", "pointPixelScale", "initWithDesktopIndependentWindow:",
		"initWithDisplay:includingApplications:exceptingWindows:"},
	"SCStream": {"initWithFilter:configuration:delegate:", "addStreamOutput:type:sampleHandlerQueue:error:",
		"startCaptureWithCompletionHandler:", "stopCaptureWithCompletionHandler:", "synchronizationClock"},
	"SCStreamConfiguration": {"init", "setWidth:", "setHeight:", "setMinimumFrameInterval:", "setPixelFormat:", "setQueueDepth:",
		"setShowsCursor:", "setSourceRect:", "setCapturesAudio:", "setSampleRate:", "setChannelCount:",
		"setExcludesCurrentProcessAudio:", "setCaptureMicrophone:", "setMicrophoneCaptureDeviceID:",
		"setColorSpaceName:", "setColorMatrix:"},

	"AVAssetWriter": {"initWithURL:fileType:error:", "setShouldOptimizeForNetworkUse:", "setMovieFragmentInterval:",
		"canApplyOutputSettings:forMediaType:", "canAddInput:", "addInput:", "startWriting", "startSessionAtSourceTime:",
		"endSessionAtSourceTime:", "finishWritingWithCompletionHandler:", "cancelWriting", "status", "error"},
	"AVAssetWriterInput": {"initWithMediaType:outputSettings:", "setExpectsMediaDataInRealTime:", "isReadyForMoreMediaData",
		"appendSampleBuffer:", "markAsFinished"},

	"+AVURLAsset":           {"URLAssetWithURL:options:"},
	"+AVAssetExportSession": {"exportSessionWithAsset:presetName:"},
	"AVAssetExportSession": {"supportedFileTypes", "setOutputURL:", "setOutputFileType:", "setShouldOptimizeForNetworkUse:",
		"setTimeRange:", "exportAsynchronouslyWithCompletionHandler:", "cancelExport", "status", "error"},
	"+AVAssetImageGenerator": {"assetImageGeneratorWithAsset:"},
	"AVAssetImageGenerator": {"setAppliesPreferredTrackTransform:", "setRequestedTimeToleranceBefore:",
		"setRequestedTimeToleranceAfter:", "setMaximumSize:", "generateCGImageAsynchronouslyForTime:completionHandler:",
		"cancelAllCGImageGeneration"},

	"+AVPlayerItem": {"playerItemWithURL:"},
	"AVPlayerItem":  {"status", "error", "duration", "addOutput:", "removeOutput:"},
	"+AVPlayer":     {"playerWithPlayerItem:"},
	"AVPlayer": {"play", "pause", "rate", "timeControlStatus", "currentTime", "seekToTime:toleranceBefore:toleranceAfter:",
		"setMuted:"},
	"AVPlayerItemVideoOutput": {"initWithPixelBufferAttributes:", "itemTimeForHostTime:", "hasNewPixelBufferForItemTime:",
		"copyPixelBufferForItemTime:itemTimeForDisplay:"},

	"+AVCaptureDeviceDiscoverySession": {"discoverySessionWithDeviceTypes:mediaType:position:"},
	"AVCaptureDeviceDiscoverySession":  {"devices"},
	"+AVCaptureDevice":                 {"defaultDeviceWithMediaType:", "authorizationStatusForMediaType:", "requestAccessForMediaType:completionHandler:", "deviceWithUniqueID:"},
	"AVCaptureDevice":                  {"uniqueID", "localizedName", "transportType", "isConnected"},
	"+AVCaptureDeviceInput":            {"deviceInputWithDevice:error:"},
	"AVCaptureSession": {"init", "beginConfiguration", "commitConfiguration", "canSetSessionPreset:", "setSessionPreset:",
		"canAddInput:", "addInput:", "canAddOutput:", "addOutput:", "startRunning", "stopRunning", "isRunning"},
	"AVCaptureVideoDataOutput": {"init", "availableVideoCVPixelFormatTypes", "setVideoSettings:", "setAlwaysDiscardsLateVideoFrames:",
		"setSampleBufferDelegate:queue:"},

	"+NSBundle":             {"mainBundle"},
	"NSBundle":              {"bundleIdentifier", "objectForInfoDictionaryKey:"},
	"+NSRunningApplication": {"currentApplication"},
	"NSRunningApplication":  {"isActive"},
	"+NSEvent":              {"modifierFlags"},

	"+NSArray":              {"array", "arrayWithObject:", "arrayWithObjects:count:"},
	"NSArray":               {"count", "objectAtIndex:"},
	"+NSString":             {"alloc"},
	"NSString":              {"initWithUTF8String:", "UTF8String"},
	"NSObject":              {"retain", "release", "autorelease"},
	"NSError":               {"localizedDescription", "code", "domain"},
	"NSDictionary":          {"objectForKey:"},
	"+NSDictionary":         {"dictionaryWithObjects:forKeys:count:"},
	"NSNumber":              {"integerValue", "unsignedIntValue", "boolValue"},
	"+NSNumber":             {"numberWithLongLong:", "numberWithDouble:", "numberWithBool:"},
	"+NSURL":                {"fileURLWithPath:"},
	"+NSWorkspace":          {"sharedWorkspace"},
	"NSWorkspace":           {"notificationCenter"},
	"+NSNotificationCenter": {"defaultCenter"},
	"NSNotificationCenter":  {"addObserverForName:object:queue:usingBlock:", "removeObserver:", "postNotificationName:object:"},
	"NSNotification":        {"object", "userInfo"},
	"+NSPasteboard":         {"generalPasteboard"},
	"NSPasteboard":          {"clearContents", "writeObjects:"},
	"+NSFileManager":        {"defaultManager"},
	"NSFileManager":         {"trashItemAtURL:resultingItemURL:error:"},
	"+SCScreenshotManager":  {"captureImageWithFilter:configuration:completionHandler:"},
	"+NSScreen":             {"screens"},
	"NSScreen":              {"deviceDescription", "localizedName"},
}

// funcs are the C functions the package calls, by library.
var funcs = map[string][]string{
	coreGraphicsPath: {"CGPreflightScreenCaptureAccess", "CGRequestScreenCaptureAccess", "CGWindowListCopyWindowInfo", "CGImageGetWidth", "CGImageGetHeight",
		"CGImageGetBytesPerRow", "CGImageGetBitsPerPixel", "CGImageGetBitmapInfo", "CGImageGetDataProvider", "CGDataProviderCopyData"},
	coreFoundationPath: {"CFRelease", "CFRetain", "CFDataGetBytePtr", "CFDataGetLength"},
	coreMediaPath: {"CMSampleBufferGetPresentationTimeStamp", "CMSampleBufferGetDuration", "CMSampleBufferGetNumSamples",
		"CMSampleBufferDataIsReady", "CMSampleBufferGetImageBuffer", "CMSampleBufferGetSampleAttachmentsArray",
		"CMSampleBufferGetFormatDescription", "CMAudioFormatDescriptionGetStreamBasicDescription",
		"CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer", "CMClockGetHostTimeClock", "CMClockGetTime",
		"CMSampleBufferCreateCopyWithNewTiming", "CMAudioFormatDescriptionCreate", "CMBlockBufferCreateWithMemoryBlock",
		"CMBlockBufferReplaceDataBytes", "CMAudioSampleBufferCreateReadyWithPacketDescriptions"},
	coreVideoPath: {"CVPixelBufferGetWidth", "CVPixelBufferGetHeight", "CVPixelBufferGetPixelFormatType", "CVPixelBufferIsPlanar",
		"CVPixelBufferGetBytesPerRow", "CVPixelBufferGetBaseAddress", "CVPixelBufferLockBaseAddress", "CVPixelBufferUnlockBaseAddress"},
}

// constants are the framework constants (extern NSString *const) the
// package reads, by library. Their values are never hard-coded: a constant
// that changed under us would raise an exception later, far from here.
var constants = map[string][]string{
	avFoundationPath: {"AVMediaTypeAudio", "AVMediaTypeVideo", "AVCaptureDeviceTypeMicrophone", "AVFileTypeMPEG4", "AVAssetExportPresetPassthrough",
		"AVCaptureDeviceTypeBuiltInWideAngleCamera", "AVCaptureDeviceTypeExternal", "AVCaptureDeviceTypeContinuityCamera",
		"AVCaptureSessionPreset1280x720", "AVCaptureDeviceWasDisconnectedNotification", "AVCaptureSessionRuntimeErrorNotification",
		"AVCaptureSessionErrorKey",
		"AVVideoCodecKey", "AVVideoCodecTypeH264", "AVVideoWidthKey", "AVVideoHeightKey", "AVVideoCompressionPropertiesKey",
		"AVVideoAverageBitRateKey", "AVVideoMaxKeyFrameIntervalDurationKey", "AVVideoAllowFrameReorderingKey",
		"AVVideoProfileLevelKey", "AVVideoProfileLevelH264HighAutoLevel", "AVVideoExpectedSourceFrameRateKey",
		"AVVideoColorPropertiesKey", "AVVideoColorPrimariesKey", "AVVideoColorPrimaries_ITU_R_709_2",
		"AVVideoTransferFunctionKey", "AVVideoTransferFunction_ITU_R_709_2", "AVVideoYCbCrMatrixKey", "AVVideoYCbCrMatrix_ITU_R_709_2"},
	coreGraphicsPath:     {"kCGColorSpaceSRGB", "kCGDisplayStreamYCbCrMatrix_ITU_R_709_2", "kCGWindowNumber", "kCGWindowIsOnscreen"},
	avfAudioPath:         {"AVFormatIDKey", "AVSampleRateKey", "AVNumberOfChannelsKey", "AVEncoderBitRateKey"},
	screenCaptureKitPath: {"SCStreamFrameInfoStatus"},
	appKitPath:           {"NSApplicationDidBecomeActiveNotification", "NSApplicationDidResignActiveNotification"},
	coreVideoPath:        {"kCVPixelBufferPixelFormatTypeKey", "kCVPixelBufferWidthKey", "kCVPixelBufferHeightKey"},
}

// protocols are the Objective-C protocols reel's classes conform to, by
// class (see protocol: reel registers them when no loaded binary has).
var protocols = map[string][]string{
	"ReelStreamOutput": {"SCStreamOutput", "SCStreamDelegate"},
	"ReelCameraOutput": {"AVCaptureVideoDataOutputSampleBufferDelegate"},
}

type cgRect struct{ X, Y, W, H float64 }

var lib struct {
	once sync.Once
	err  error

	preflight, request func() bool
	cfRelease          func(uintptr)
	cfRetain           func(uintptr) uintptr
	poolPush           func() uintptr
	poolPop            func(uintptr)

	consts map[string]objc.ID
}

// load opens the frameworks and resolves every function and constant once.
func load() error {
	lib.once.Do(func() { lib.err = loadOnce() })
	return lib.err
}

func loadOnce() error {
	handles := map[string]uintptr{}
	for _, path := range []string{foundationPath, coreFoundationPath, coreGraphicsPath, coreMediaPath, coreVideoPath, avfAudioPath, avFoundationPath, appKitPath, screenCaptureKitPath} {
		h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return fmt.Errorf("mac: %w", err)
		}
		handles[path] = h
	}
	for path, names := range funcs {
		for _, name := range names {
			if _, err := purego.Dlsym(handles[path], name); err != nil {
				return fmt.Errorf("mac: %w", err)
			}
		}
	}
	cg, cf := handles[coreGraphicsPath], handles[coreFoundationPath]
	purego.RegisterLibFunc(&lib.preflight, cg, "CGPreflightScreenCaptureAccess")
	purego.RegisterLibFunc(&lib.request, cg, "CGRequestScreenCaptureAccess")
	purego.RegisterLibFunc(&lib.cfRelease, cf, "CFRelease")
	purego.RegisterLibFunc(&lib.cfRetain, cf, "CFRetain")
	for name, fn := range map[string]any{"objc_autoreleasePoolPush": &lib.poolPush, "objc_autoreleasePoolPop": &lib.poolPop} {
		p, err := purego.Dlsym(purego.RTLD_DEFAULT, name)
		if err != nil {
			return fmt.Errorf("mac: %w", err)
		}
		purego.RegisterFunc(fn, p)
	}

	if err := loadCoreMedia(); err != nil {
		return fmt.Errorf("mac: %w", err)
	}
	if err := loadWriter(); err != nil {
		return fmt.Errorf("mac: %w", err)
	}
	if err := loadAudioWriter(); err != nil {
		return fmt.Errorf("mac: %w", err)
	}
	if err := loadWindows(); err != nil {
		return fmt.Errorf("mac: %w", err)
	}
	if err := loadScreenshot(); err != nil {
		return fmt.Errorf("mac: %w", err)
	}
	if err := loadPlayer(); err != nil {
		return fmt.Errorf("mac: %w", err)
	}
	lib.consts = map[string]objc.ID{}
	for path, names := range constants {
		for _, name := range names {
			p, err := purego.Dlsym(handles[path], name)
			if err != nil {
				return fmt.Errorf("mac: %w", err)
			}
			v := objc.ID(deref(p))
			if v == 0 {
				return fmt.Errorf("mac: constant %s is nil", name)
			}
			lib.consts[name] = v
		}
	}
	for class := range selectors {
		name := strings.TrimPrefix(class, "+")
		if objc.GetClass(name) == 0 {
			return fmt.Errorf("mac: no class %s (reel needs macOS 15 or later)", name)
		}
	}
	return nil
}

// constant is a framework constant resolved by load.
func constant(name string) objc.ID {
	v, ok := lib.consts[name]
	if !ok {
		panic("mac: constant " + name + " isn't in the constants table")
	}
	return v
}

// deref reads the pointer stored at address p (an extern variable's
// value). Going through &p keeps vet's unsafe.Pointer check satisfied:
// the address is never converted from a bare uintptr.
func deref(p uintptr) uintptr {
	return **(**uintptr)(unsafe.Pointer(&p))
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// withPool runs fn on a locked OS thread inside an autorelease pool, so
// autoreleased objects fn makes are freed when it returns.
func withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := lib.poolPush()
	defer lib.poolPop(pool)
	fn()
}

// nsString makes an autoreleased NSString (inside withPool).
func nsString(s string) objc.ID {
	return class("NSString").Send(sel("alloc")).Send(sel("initWithUTF8String:"), s).Send(sel("autorelease"))
}

// goString copies an NSString; nil is "".
func goString(s objc.ID) string {
	if s == 0 {
		return ""
	}
	return cString(objc.Send[*byte](s, sel("UTF8String")))
}

// cString copies a NUL-terminated C string.
func cString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

// fileURL makes an autoreleased file NSURL (inside withPool).
func fileURL(path string) objc.ID {
	return class("NSURL").Send(sel("fileURLWithPath:"), nsString(path))
}

// nsArray makes an autoreleased NSArray of ids (inside withPool).
func nsArray(ids ...objc.ID) objc.ID {
	if len(ids) == 0 {
		return class("NSArray").Send(sel("array"))
	}
	return class("NSArray").Send(sel("arrayWithObjects:count:"), unsafe.Pointer(&ids[0]), uint(len(ids)))
}

// each calls fn with every element of an NSArray.
func each(arr objc.ID, fn func(objc.ID)) {
	if arr == 0 {
		return
	}
	n := objc.Send[uint](arr, sel("count"))
	for i := range n {
		fn(arr.Send(sel("objectAtIndex:"), i))
	}
}

// nsError describes an NSError as a Go error.
func nsError(what string, err objc.ID) error {
	if err == 0 {
		return fmt.Errorf("%s: no error given", what)
	}
	return &Error{
		What:   what,
		Domain: goString(err.Send(sel("domain"))),
		Code:   objc.Send[int](err, sel("code")),
		Desc:   goString(err.Send(sel("localizedDescription"))),
	}
}

// Error is an NSError from a framework call.
type Error struct {
	What   string // what reel was doing
	Domain string
	Code   int
	Desc   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (%s %d)", e.What, e.Desc, e.Domain, e.Code)
}

// errTimeout is a completion handler that never came.
var errTimeout = errors.New("timed out")
