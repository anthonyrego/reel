// Package mixer puts system audio and a microphone on one 48 kHz stereo
// timeline and sums them into the recording's single audio track. Pure.
//
// Each source arrives in buffers stamped with their place in the recording
// (from the samples' PTS, through the timeline). A source's own clock
// never quite matches the host clock its PTS come from: around 100 ppm
// apart is common, about 180 ms over half an hour. So each source runs
// through a resampler whose ratio a controller nudges, at most 0.1%, to
// keep where the samples land equal to where their PTS say they belong.
// Only a real gap (over 50 ms off: a device that stopped, or a pause in
// the source) re-anchors the source, leaving silence.
//
// Emission is driven by the caller's clock, not by arrivals: Emit writes
// every whole block up to a time the caller chooses (now minus a jitter
// window), and whatever a source hasn't delivered by then is silence. A
// source that stops delivering (system audio while nothing plays, a mic
// unplugged) never holds the recording back.
package mixer

import "math"

// Rate is the recording's sample rate; output is stereo float32.
const Rate = 48000

// Block is the frames Emit hands over at a time (AAC's frame).
const Block = 1024

// Source names an input.
type Source int

const (
	System Source = iota
	Mic
	numSources
)

func (s Source) String() string {
	if s == Mic {
		return "mic"
	}
	return "system"
}

const (
	// maxAdjust bounds how far the controller bends a source's rate.
	maxAdjust = 0.001
	// correctIn is the controller's time constant: an error is worked off
	// over about this long. With 300 ppm of drift it settles about
	// 300e-6 × 48000 × 2 ≈ 29 frames (0.6 ms) behind.
	correctIn = 2.0
	// reanchorAt is how far off (frames) a source may be before it jumps
	// instead of bending: 50 ms.
	reanchorAt = Rate / 20
)

// Mixer places and sums the sources. The zero value isn't usable; use New.
type Mixer struct {
	// Gain scales each source before the sum.
	Gain [numSources]float32

	srcs    [numSources]*source
	emitted int64 // frames emitted so far
	block   []float32

	// Late counts input frames that arrived after their place had been
	// emitted (they're dropped); Reanchors counts jumps.
	Late, Reanchors [numSources]int64
}

// New returns a mixer with both gains at 1.
func New() *Mixer {
	return &Mixer{Gain: [numSources]float32{1, 1}, block: make([]float32, 2*Block)}
}

// Emitted is how many frames have been emitted: where the next block
// starts.
func (m *Mixer) Emitted() int64 { return m.emitted }

// source is one input's resampler and its stretch of output.
type source struct {
	chans int
	// hist holds input not yet consumed, per channel; hist[c][0] is the
	// sample before the one at floor(inPos), for the cubic.
	hist     [][]float32
	inPos    float64 // fractional index into hist of the next output frame
	next     int64   // output frame the next output sample goes to
	adj      float64 // the controller's rate bend
	err      float64 // the last placement error, frames
	reanchor bool    // jump to the next buffer's place (after a pause)
	// out is this source's output from the mixer's emitted frame on,
	// interleaved stereo; frames it never wrote stay zero.
	out []float32
}

// Push adds a buffer of a source: planar channels (one or two; a mono
// source is centered) at rate, whose first frame belongs at output frame
// pos (fractional, from its PTS). Buffers of a source come in order.
func (m *Mixer) Push(src Source, pos, rate float64, in [][]float32) {
	if len(in) == 0 || len(in[0]) == 0 || rate <= 0 {
		return
	}
	chans := min(len(in), 2)
	s := m.srcs[src]
	if s == nil || s.chans != chans {
		s = &source{chans: chans, hist: make([][]float32, chans), inPos: 1}
		for c := range chans {
			s.hist[c] = []float32{in[c][0]} // the sample before the first
		}
		s.next = int64(math.Round(pos))
		m.srcs[src] = s
	} else {
		ratio := Rate / rate * (1 + s.adj)
		pending := float64(len(s.hist[0])) - s.inPos
		predicted := float64(s.next) + pending*ratio
		s.err = pos - predicted
		if s.reanchor || math.Abs(s.err) > reanchorAt {
			s.next += int64(math.Round(s.err))
			if !s.reanchor {
				m.Reanchors[src]++
			}
			s.adj, s.err, s.reanchor = 0, 0, false
		} else {
			s.adj = max(-maxAdjust, min(maxAdjust, s.err/(Rate*correctIn)))
		}
	}
	for c := range chans {
		s.hist[c] = append(s.hist[c], in[c][:len(in[0])]...)
	}
	m.run(src, s, Rate/rate*(1+s.adj))
}

// run resamples as much of the source's input as it can into its output.
func (m *Mixer) run(src Source, s *source, ratio float64) {
	step := 1 / ratio
	n := len(s.hist[0])
	for {
		i := int(s.inPos)
		if i+2 >= n {
			break
		}
		f := float32(s.inPos - float64(i))
		at := s.next - m.emitted
		if at < 0 {
			m.Late[src]++
		} else {
			need := int(at+1) * 2
			if need > len(s.out) {
				s.out = append(s.out, make([]float32, need-len(s.out))...)
			}
			l := cubic(s.hist[0], i, f)
			r := l
			if s.chans == 2 {
				r = cubic(s.hist[1], i, f)
			}
			s.out[2*at], s.out[2*at+1] = l, r
		}
		s.next++
		s.inPos += step
	}
	// Keep the sample before floor(inPos) and everything after it.
	if drop := int(s.inPos) - 1; drop > 0 {
		for c := range s.hist {
			s.hist[c] = append(s.hist[c][:0], s.hist[c][drop:]...)
		}
		s.inPos -= float64(drop)
	}
}

// cubic interpolates x between x[i] and x[i+1] at f (0..1) through
// x[i-1]..x[i+2] (a Catmull-Rom spline): exact at f = 0, and gentle on
// the highs where a straight line would dull them.
func cubic(x []float32, i int, f float32) float32 {
	if f == 0 {
		return x[i]
	}
	p0, p1, p2, p3 := x[i-1], x[i], x[i+1], x[i+2]
	a := -0.5*p0 + 1.5*p1 - 1.5*p2 + 0.5*p3
	b := p0 - 2.5*p1 + 2*p2 - 0.5*p3
	c := -0.5*p0 + 0.5*p2
	return ((a*f+b)*f+c)*f + p1
}

// Emit hands fn every whole block that ends at or before frame upTo, in
// order: start is the block's first frame, block its frames (interleaved
// stereo, valid only during fn).
func (m *Mixer) Emit(upTo int64, fn func(start int64, block []float32)) {
	for m.emitted+Block <= upTo {
		m.emitBlock(Block, fn)
	}
}

// Flush hands fn everything up to frame end, the last block perhaps
// short: the recording's end.
func (m *Mixer) Flush(end int64, fn func(start int64, block []float32)) {
	m.Emit(end, fn)
	if n := end - m.emitted; n > 0 {
		m.emitBlock(int(n), fn)
	}
}

func (m *Mixer) emitBlock(n int, fn func(int64, []float32)) {
	b := m.block[:2*n]
	clear(b)
	for i, s := range m.srcs {
		if s == nil {
			continue
		}
		g := m.Gain[i]
		k := min(len(s.out), 2*n)
		for j := 0; j < k; j++ {
			b[j] += g * s.out[j]
		}
		// The emitted frames leave the source's output.
		if k < len(s.out) {
			s.out = append(s.out[:0], s.out[k:]...)
		} else {
			s.out = s.out[:0]
		}
	}
	for j, v := range b {
		b[j] = limit(v)
	}
	fn(m.emitted, b)
	m.emitted += int64(n)
}

// knee is where the limiter starts: a sum above it bends smoothly toward
// the ceiling instead of clipping. The ceiling (−0.18 dBFS) leaves the AAC
// encoder room to overshoot.
const (
	knee    = 0.9
	ceiling = 0.98
)

// limit is a soft limiter: unchanged up to the knee, then a tanh curve
// toward the ceiling.
func limit(v float32) float32 {
	a := float32(math.Abs(float64(v)))
	if a <= knee {
		return v
	}
	y := knee + (ceiling-knee)*float32(math.Tanh(float64((a-knee)/(ceiling-knee))))
	if v < 0 {
		return -y
	}
	return y
}

// Lag is how far a source's last buffer landed from where its PTS put it,
// in frames (positive: the samples belonged later). For logs and tests.
func (m *Mixer) Lag(src Source) float64 {
	if s := m.srcs[src]; s != nil {
		return s.err
	}
	return 0
}

// Bend is how much a source's rate is being bent (0.0001 is 100 ppm).
func (m *Mixer) Bend(src Source) float64 {
	if s := m.srcs[src]; s != nil {
		return s.adj
	}
	return 0
}

// Reanchor makes each source land exactly where its next buffer's PTS
// says, instead of bending toward it: after a pause, where a buffer that
// straddled the pause's start ran a few milliseconds past it.
func (m *Mixer) Reanchor() {
	for _, s := range m.srcs {
		if s != nil {
			s.reanchor = true
		}
	}
}
