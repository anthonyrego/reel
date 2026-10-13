// Package timeline maps capture time to recording time.
//
// ScreenCaptureKit stamps every sample with host time. A recording starts
// at zero at its first video frame, and leaves out every pause. Samples
// arrive late (the mic tens of milliseconds after its PTS), so a pause is a
// span of host time and each sample is placed by its own PTS, never by
// when it arrived: one captured just before Pause but delivered after it
// is kept, and one captured inside a pause is dropped whenever it comes.
//
// Times are nanoseconds. Pure.
package timeline

import "math"

// open is the end of a pause that hasn't been resumed.
const open = math.MaxInt64

type span struct{ from, to int64 }

// Timeline maps host time to recording time. The zero value hasn't
// started.
type Timeline struct {
	started bool
	t0      int64
	pauses  []span // in order, the last perhaps open
}

// Start sets the host time that is the recording's zero. Only the first
// call counts.
func (t *Timeline) Start(t0 int64) {
	if !t.started {
		t.started, t.t0 = true, t0
	}
}

// Started reports whether Start was called.
func (t *Timeline) Started() bool { return t.started }

// Paused reports whether the last pause is still open.
func (t *Timeline) Paused() bool {
	return len(t.pauses) > 0 && t.pauses[len(t.pauses)-1].to == open
}

// Pause opens a pause at host time at. Pausing while paused, or at a time
// before the last pause ended, does nothing.
func (t *Timeline) Pause(at int64) {
	if t.Paused() {
		return
	}
	if n := len(t.pauses); n > 0 && at < t.pauses[n-1].to {
		return
	}
	t.pauses = append(t.pauses, span{at, open})
}

// Resume closes the open pause at host time at.
func (t *Timeline) Resume(at int64) {
	if !t.Paused() {
		return
	}
	p := &t.pauses[len(t.pauses)-1]
	p.to = max(at, p.from)
}

// Map is the recording time of a sample captured at host time pts, and
// whether to keep it: samples before the start or inside a pause are
// dropped.
func (t *Timeline) Map(pts int64) (int64, bool) {
	if !t.started || pts < t.t0 {
		return 0, false
	}
	var paused int64
	for _, p := range t.pauses {
		switch {
		case pts < p.from:
			return pts - t.t0 - paused, true
		case pts < p.to:
			return 0, false
		default:
			paused += p.to - p.from
		}
	}
	return pts - t.t0 - paused, true
}

// At is the recording time at host time at: as Map, but a time inside a
// pause is where the pause began, and one before the start is zero. It
// places the end of a recording that stops while paused.
func (t *Timeline) At(at int64) int64 {
	if !t.started || at < t.t0 {
		return 0
	}
	var paused int64
	for _, p := range t.pauses {
		switch {
		case at < p.from:
			return at - t.t0 - paused
		case at < p.to:
			return p.from - t.t0 - paused
		default:
			paused += p.to - p.from
		}
	}
	return at - t.t0 - paused
}

// PausedFor is the total paused time, counting an open pause up to host
// time now.
func (t *Timeline) PausedFor(now int64) int64 {
	var d int64
	for _, p := range t.pauses {
		to := p.to
		if to == open {
			to = now
		}
		if to > p.from {
			d += to - p.from
		}
	}
	return d
}
