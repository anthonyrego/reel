package mixer

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"testing"
)

// feeder pushes a source as a device would: buffers of chunk frames at a
// nominal rate, while the device's real clock runs at trueRate, so each
// buffer's PTS (its true time) drifts from its frame count.
type feeder struct {
	m        *Mixer
	src      Source
	rate     float64 // what the device says
	trueRate float64 // what it runs at
	chunk    int
	n        int64 // frames pushed
	jitter   float64
	rng      *rand.Rand
}

// push sends the next buffer, its samples from gen(n) (n counts frames
// from the start), and returns the buffer's true start time.
func (f *feeder) push(gen func(n int64) float32) float64 {
	buf := make([]float32, f.chunk)
	for i := range buf {
		buf[i] = gen(f.n + int64(i))
	}
	t := float64(f.n) / f.trueRate
	pos := t * Rate
	if f.jitter > 0 {
		pos += (f.rng.Float64()*2 - 1) * f.jitter * Rate
	}
	f.m.Push(f.src, pos, f.rate, [][]float32{buf})
	f.n += int64(f.chunk)
	return t
}

func sine(hz, rate float64) func(int64) float32 {
	return func(n int64) float32 { return float32(0.25 * math.Sin(2*math.Pi*hz*float64(n)/rate)) }
}

// TestPassthrough: a 48 kHz stereo source stamped exactly comes out
// sample for sample.
func TestPassthrough(t *testing.T) {
	m := New()
	const n = 2 * Rate
	l, r := make([]float32, n), make([]float32, n)
	for i := range n {
		l[i] = float32(math.Sin(float64(i) * 0.01))
		r[i] = float32(math.Cos(float64(i) * 0.013))
	}
	for at := 0; at < n; at += 960 {
		m.Push(System, float64(at), Rate, [][]float32{l[at : at+960], r[at : at+960]})
	}
	var out []float32
	m.Flush(n-2, func(_ int64, b []float32) { out = append(out, b...) })
	for i := 0; i < n-2; i++ {
		if out[2*i] != limit(l[i]) || out[2*i+1] != limit(r[i]) {
			t.Fatalf("frame %d: %v,%v want %v,%v", i, out[2*i], out[2*i+1], l[i], r[i])
		}
	}
	if m.Bend(System) != 0 || m.Reanchors[System] != 0 {
		t.Errorf("bend %v, reanchors %d", m.Bend(System), m.Reanchors[System])
	}
}

// TestDrift: a mic whose clock runs ±300 ppm off stays within 2 ms of
// where its PTS put it over 30 minutes, without a jump.
func TestDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("30 simulated minutes")
	}
	for _, ppm := range []float64{300, -300} {
		m := New()
		f := &feeder{m: m, src: Mic, rate: Rate, trueRate: Rate * (1 + ppm*1e-6), chunk: 480}
		const minutes = 30
		mark := int64(29.5 * 60 * f.trueRate) // an impulse at 29.5 minutes, true time
		found, peak := int64(-1), float32(0)
		look := func(start int64, b []float32) {
			for i := 0; i < len(b); i += 2 {
				if v := b[i]; v > peak {
					peak, found = v, start+int64(i/2)
				}
			}
		}
		gen := func(n int64) float32 {
			if n == mark {
				return 0.8
			}
			return 0
		}
		for float64(f.n) < minutes*60*f.trueRate {
			tt := f.push(gen)
			m.Emit(int64((tt-0.15)*Rate), look)
		}
		m.Flush(int64(minutes*60*Rate), look)
		want := float64(mark) / f.trueRate * Rate
		if math.Abs(float64(found)-want) > 96 {
			t.Errorf("%+.0f ppm: the impulse came out at frame %d, its PTS says %.0f (%.2f ms off)", ppm, found, want, (float64(found)-want)/48)
		}
		if m.Reanchors[Mic] != 0 {
			t.Errorf("%+.0f ppm: %d jumps", ppm, m.Reanchors[Mic])
		}
		if lag := m.Lag(Mic); math.Abs(lag) > 96 {
			t.Errorf("%+.0f ppm: settled %.1f frames off", ppm, lag)
		}
		t.Logf("%+.0f ppm: impulse %.2f ms off, settled %.1f frames off, bend %.0f ppm", ppm, (float64(found)-want)/48, m.Lag(Mic), m.Bend(Mic)*1e6)
	}
}

// TestRateConversion: a 44.1 kHz mic comes out at the same pitch and level.
func TestRateConversion(t *testing.T) {
	m := New()
	f := &feeder{m: m, src: Mic, rate: 44100, trueRate: 44100, chunk: 441}
	var out []float32
	for f.n < 3*44100 {
		f.push(sine(1000, 44100))
	}
	m.Flush(3*Rate-200, func(_ int64, b []float32) { out = append(out, b...) })
	// One second from the middle, left channel.
	seg := make([]float32, Rate)
	for i := range seg {
		seg[i] = out[2*(Rate+i)]
	}
	crossings := 0
	var sum float64
	for i := 1; i < len(seg); i++ {
		if seg[i-1] < 0 && seg[i] >= 0 {
			crossings++
		}
		sum += float64(seg[i]) * float64(seg[i])
	}
	if crossings < 998 || crossings > 1002 {
		t.Errorf("%d cycles in a second, want 1000", crossings)
	}
	rms := math.Sqrt(sum / float64(len(seg)))
	if db := 20 * math.Log10(rms/(0.25/math.Sqrt2)); math.Abs(db) > 0.5 {
		t.Errorf("level off by %.2f dB", db)
	}
}

// TestJitter: PTS that wobble ±5 ms bend the rate a little but never jump.
func TestJitter(t *testing.T) {
	m := New()
	f := &feeder{m: m, src: Mic, rate: Rate, trueRate: Rate, chunk: 480, jitter: 0.005, rng: rand.New(rand.NewPCG(1, 2))}
	for f.n < 60*Rate {
		tt := f.push(sine(440, Rate))
		m.Emit(int64((tt-0.15)*Rate), func(int64, []float32) {})
	}
	if m.Reanchors[Mic] != 0 {
		t.Errorf("%d jumps", m.Reanchors[Mic])
	}
	if math.Abs(m.Lag(Mic)) > 0.006*Rate {
		t.Errorf("%.0f frames off", m.Lag(Mic))
	}
}

// TestGap: a source that stops for half a second jumps over it, leaving
// silence, and what follows lands where it belongs.
func TestGap(t *testing.T) {
	m := New()
	var out []float32
	collect := func(_ int64, b []float32) { out = append(out, b...) }
	one := func(at float64, n int, v float32) {
		buf := make([]float32, n)
		for i := range buf {
			buf[i] = v
		}
		m.Push(Mic, at, Rate, [][]float32{buf})
	}
	for at := 0; at < Rate; at += 480 { // the first second
		one(float64(at), 480, 0.1)
	}
	for at := Rate * 3 / 2; at < Rate*2; at += 480 { // after a 0.5 s gap
		one(float64(at), 480, 0.2)
	}
	m.Flush(2*Rate-480, collect)
	if m.Reanchors[Mic] != 1 {
		t.Errorf("%d jumps, want 1", m.Reanchors[Mic])
	}
	if v := out[2*(Rate+Rate/4)]; v != 0 {
		t.Errorf("the gap has %v, want silence", v)
	}
	if v := out[2*(Rate*3/2+10)]; v != 0.2 {
		t.Errorf("after the gap %v, want 0.2", v)
	}
	if v := out[2*(Rate*3/2-10)]; v != 0 {
		t.Errorf("just before the resumed audio %v, want silence", v)
	}
}

// TestLateAndAbsent: emission doesn't wait for a source; samples whose
// place was already emitted are dropped and counted.
func TestLateAndAbsent(t *testing.T) {
	m := New()
	got := 0
	m.Emit(Rate, func(_ int64, b []float32) {
		got += len(b) / 2
		for _, v := range b {
			if v != 0 {
				t.Fatal("silence expected with no sources")
			}
		}
	})
	if got != Rate/Block*Block {
		t.Errorf("emitted %d frames", got)
	}
	m.Push(Mic, Rate/2, Rate, [][]float32{make([]float32, 4800)})
	if m.Late[Mic] == 0 {
		t.Error("late samples weren't counted")
	}
}

// TestLimiter: two loud sources never sum past full scale; quiet ones pass
// untouched.
func TestLimiter(t *testing.T) {
	m := New()
	loud := make([]float32, 4800)
	for i := range loud {
		loud[i] = 0.95
	}
	m.Push(System, 0, Rate, [][]float32{loud, loud})
	m.Push(Mic, 0, Rate, [][]float32{loud})
	m.Flush(4000, func(_ int64, b []float32) {
		for _, v := range b {
			if v > ceiling || v <= knee {
				t.Fatalf("%v", v)
			}
		}
	})
	if limit(0.5) != 0.5 || limit(-0.9) != -0.9 {
		t.Error("the limiter touched a quiet sample")
	}
}

func TestDecode(t *testing.T) {
	// int16 interleaved stereo, little endian: keep both, then just the first.
	b := make([]byte, 8)
	binary.LittleEndian.PutUint16(b[0:], uint16(16384))  // L 0.5
	binary.LittleEndian.PutUint16(b[2:], uint16(0xC000)) // R −0.5
	binary.LittleEndian.PutUint16(b[4:], uint16(32767))  // L ~1
	binary.LittleEndian.PutUint16(b[6:], 0)              // R 0
	out, err := Decode(Format{Rate: 48000, Channels: 2, Bits: 16, Interleaved: true}, [][]byte{b}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[0][0] != 0.5 || out[1][0] != -0.5 || len(out[0]) != 2 {
		t.Errorf("int16: %v", out)
	}
	out, _ = Decode(Format{Rate: 48000, Channels: 2, Bits: 16, Interleaved: true}, [][]byte{b}, 1, out)
	if len(out) != 1 || out[0][1] < 0.99 {
		t.Errorf("keep 1: %v", out)
	}
	// float32 planar, two buffers.
	l, r := make([]byte, 4), make([]byte, 4)
	binary.LittleEndian.PutUint32(l, math.Float32bits(0.25))
	binary.LittleEndian.PutUint32(r, math.Float32bits(-0.75))
	out, err = Decode(Format{Rate: 48000, Channels: 2, Bits: 32, Float: true}, [][]byte{l, r}, 2, nil)
	if err != nil || out[0][0] != 0.25 || out[1][0] != -0.75 {
		t.Errorf("float planar: %v %v", out, err)
	}
	// int24 big endian mono.
	out, err = Decode(Format{Rate: 16000, Channels: 1, Bits: 24, BigEndian: true, Interleaved: true}, [][]byte{{0x40, 0, 0}}, 1, nil)
	if err != nil || out[0][0] != 0.5 {
		t.Errorf("int24: %v %v", out, err)
	}
	if _, err := Decode(Format{Channels: 1, Bits: 8}, [][]byte{{1}}, 1, nil); err == nil {
		t.Error("8-bit read without an error")
	}
}

// TestReanchor: after Reanchor a source lands exactly at its next PTS,
// even a few milliseconds back, and it isn't counted as a gap.
func TestReanchor(t *testing.T) {
	m := New()
	one := func(at float64, v float32) {
		buf := make([]float32, 960)
		for i := range buf {
			buf[i] = v
		}
		m.Push(Mic, at, Rate, [][]float32{buf})
	}
	one(0, 0.1)
	one(960, 0.1)
	m.Reanchor()
	one(960+480, 0.3) // 10 ms back: overwrites the tail of the last buffer
	var out []float32
	m.Flush(2400, func(_ int64, b []float32) { out = append(out, b...) })
	if v := out[2*1500]; v != 0.3 {
		t.Errorf("frame 1500 = %v, want 0.3", v)
	}
	if m.Reanchors[Mic] != 0 {
		t.Errorf("counted %d jumps", m.Reanchors[Mic])
	}
}
