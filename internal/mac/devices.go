package mac

import (
	"github.com/ebitengine/purego/objc"
)

// Permission is the state of a privacy permission (TCC). For a binary run
// from a terminal it is the terminal's.
type Permission int

const (
	PermissionUnknown Permission = iota // not asked yet
	PermissionDenied                    // denied, or restricted by policy
	PermissionGranted
)

func (p Permission) String() string {
	switch p {
	case PermissionGranted:
		return "granted"
	case PermissionDenied:
		return "denied"
	}
	return "not asked"
}

// ScreenAccess reports the Screen Recording permission. It never asks; a
// process that hasn't asked yet reads as denied (CoreGraphics can't tell).
func ScreenAccess() Permission {
	if load() != nil || !lib.preflight() {
		return PermissionDenied
	}
	return PermissionGranted
}

// RequestScreenAccess asks for the Screen Recording permission: macOS
// shows its prompt the first time and only says no after that. A grant
// takes effect when the app is started again.
func RequestScreenAccess() bool {
	return load() == nil && lib.request()
}

// AVAuthorizationStatus values (AVCaptureDevice.h).
const (
	avNotDetermined = 0
	avRestricted    = 1
	avDenied        = 2
	avAuthorized    = 3
)

// MicAccess reports the Microphone permission without asking.
func MicAccess() Permission { return access("AVMediaTypeAudio") }

// access reports the permission for a capture device's media type
// without asking.
func access(media string) Permission {
	if load() != nil {
		return PermissionDenied
	}
	var st int
	withPool(func() {
		st = objc.Send[int](class("AVCaptureDevice"), sel("authorizationStatusForMediaType:"), constant(media))
	})
	switch st {
	case avAuthorized:
		return PermissionGranted
	case avNotDetermined:
		return PermissionUnknown
	}
	return PermissionDenied
}

// Mic is a microphone the recording can take.
type Mic struct {
	// ID is AVCaptureDevice's uniqueID: what ScreenCaptureKit's
	// microphoneCaptureDeviceID takes, and stable across launches.
	ID        string
	Name      string
	Transport Transport
	Default   bool // the system's default input
}

// Transport is how a device is attached (CoreAudio's
// kAudioDevicePropertyTransportType, a four-character code).
type Transport uint32

const (
	TransportBuiltIn   Transport = 'b'<<24 | 'l'<<16 | 't'<<8 | 'n'
	TransportUSB       Transport = 'u'<<24 | 's'<<16 | 'b'<<8 | ' '
	TransportBluetooth Transport = 'b'<<24 | 'l'<<16 | 'u'<<8 | 'e'
	TransportBLE       Transport = 'b'<<24 | 'l'<<16 | 'e'<<8 | 'o'
	TransportVirtual   Transport = 'v'<<24 | 'i'<<16 | 'r'<<8 | 't'
	TransportAggregate Transport = 'g'<<24 | 'r'<<16 | 'u'<<8 | 'p'
	// TransportContinuity is an iPhone used as the Mac's mic (Continuity).
	TransportContinuity Transport = 'c'<<24 | 'c'<<16 | 'w'<<8 | 'd'
)

// Bluetooth reports a Bluetooth device: taking its mic switches a headset
// into its low-quality call mode for as long as it's open.
func (t Transport) Bluetooth() bool { return t == TransportBluetooth || t == TransportBLE }

func (t Transport) String() string {
	switch t {
	case TransportBuiltIn:
		return "built-in"
	case TransportUSB:
		return "usb"
	case TransportBluetooth, TransportBLE:
		return "bluetooth"
	case TransportVirtual:
		return "virtual"
	case TransportAggregate:
		return "aggregate"
	case TransportContinuity:
		return "continuity"
	case 0:
		return "unknown"
	}
	b := []byte{byte(t >> 24), byte(t >> 16), byte(t >> 8), byte(t)}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return "unknown"
		}
	}
	return string(b)
}

// Mics lists the microphones. Listing doesn't open a device, so it never
// shows the Microphone prompt.
func Mics() ([]Mic, error) {
	if err := load(); err != nil {
		return nil, err
	}
	var mics []Mic
	withPool(func() {
		types := nsArray(constant("AVCaptureDeviceTypeMicrophone"))
		session := class("AVCaptureDeviceDiscoverySession").Send(sel("discoverySessionWithDeviceTypes:mediaType:position:"),
			types, constant("AVMediaTypeAudio"), 0)
		def := ""
		if d := class("AVCaptureDevice").Send(sel("defaultDeviceWithMediaType:"), constant("AVMediaTypeAudio")); d != 0 {
			def = goString(d.Send(sel("uniqueID")))
		}
		each(session.Send(sel("devices")), func(d objc.ID) {
			if !objc.Send[bool](d, sel("isConnected")) {
				return
			}
			id := goString(d.Send(sel("uniqueID")))
			mics = append(mics, Mic{
				ID:        id,
				Name:      goString(d.Send(sel("localizedName"))),
				Transport: Transport(objc.Send[int32](d, sel("transportType"))),
				Default:   id == def,
			})
		})
	})
	return mics, nil
}

// RequestMicAccess asks for the Microphone permission: macOS shows its
// prompt the first time and answers from the stored choice after that.
// done is called with the answer on an AVFoundation queue. It opens no
// device.
func RequestMicAccess(done func(granted bool)) error { return requestAccess("AVMediaTypeAudio", done) }

// requestAccess asks for the permission for a capture device's media type
// (macOS prompts only the first time); done gets the answer on an
// AVFoundation queue.
func requestAccess(media string, done func(granted bool)) error {
	if err := load(); err != nil {
		return err
	}
	var block objc.Block
	block = objc.NewBlock(func(_ objc.Block, granted bool) {
		done(granted)
		block.Release()
	})
	withPool(func() {
		class("AVCaptureDevice").Send(sel("requestAccessForMediaType:completionHandler:"), constant(media), block)
	})
	return nil
}
