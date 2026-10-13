package main

// probe (M1): runs a ScreenCaptureKit stream and reports what arrives,
// writing nothing. It answers what the writer depends on: which frame
// statuses come and in what order, what the audio formats are, whether
// the samples' clocks agree with the host clock, and whether system audio
// keeps coming while nothing plays.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Zyko0/go-sdl3/sdl"

	"github.com/anthonyrego/reel/internal/area"
	"github.com/anthonyrego/reel/internal/config"
	"github.com/anthonyrego/reel/internal/gfx"
	"github.com/anthonyrego/reel/internal/logx"
	"github.com/anthonyrego/reel/internal/mac"
	"github.com/anthonyrego/reel/internal/platform"
)

func init() {
	register(&command{name: "probe", summary: "record to nowhere and report what ScreenCaptureKit delivers (M1)", run: runProbe})
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	self := fs.Bool("self", false, "record reel's own flashing, clicking window (agents: this one only)")
	display := fs.String("display", "", "user only: record this display (an ID from devices, or main)")
	window := fs.Uint("window", 0, "user only: record this window (an ID from devices)")
	app := fs.String("app", "", "user only: record the first on-screen window of the app whose name contains this")
	mic := fs.String("mic", "", "user only: also record this mic: an ID from devices, or default")
	systemAudio := fs.Bool("system-audio", true, "record system audio")
	dur := fs.Duration("for", 6*time.Second, "how long to record")
	clicksFor := fs.Duration("clicks-for", 0, "with -self: click for this long, then close the speakers (default: half of -for)")
	fps := fs.Int("fps", 30, "the most frames a second")
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := 0
	for _, set := range []bool{*self, *display != "", *window != 0, *app != ""} {
		if set {
			targets++
		}
	}
	if targets != 1 {
		return errors.New("pick one of -self, -display, -window or -app")
	}
	if *clicksFor == 0 {
		*clicksFor = *dur / 2
	}
	paths, err := config.Resolve()
	if err != nil {
		return err
	}
	logf, err := logx.Setup(paths.Logs, "probe", false)
	if err != nil {
		return err
	}
	defer logf.Close()

	cfg := mac.StreamConfig{FPS: *fps, ShowsCursor: true, SystemAudio: *systemAudio, Mic: *mic}
	if *self {
		cfg.IncludeOwnAudio = true
		return probeSelf(cfg, *dur, *clicksFor)
	}
	content, err := mac.ShareableContent()
	if err != nil {
		return err
	}
	if err := pickTarget(&cfg, content, *display, uint32(*window), *app); err != nil {
		return err
	}
	return probe(cfg, *dur, os.Stdout)
}

// pickTarget points cfg at a display or window and sizes it.
func pickTarget(cfg *mac.StreamConfig, content mac.Content, display string, window uint32, app string) error {
	if display != "" {
		var id uint32
		if display != "main" {
			n, err := strconv.ParseUint(display, 10, 32)
			if err != nil {
				return fmt.Errorf("-display %q: an ID or main", display)
			}
			id = uint32(n)
		}
		for _, d := range content.Displays {
			if id == 0 || d.ID == id {
				cfg.Target = mac.Target{DisplayID: d.ID}
				cfg.Width, cfg.Height = area.Pixels(d.Frame.W, d.Frame.H, d.Scale, area.MaxW, area.MaxH)
				return nil
			}
		}
		return fmt.Errorf("no display %s", display)
	}
	for _, w := range content.Windows {
		if (window != 0 && w.ID == window) || (app != "" && w.OnScreen && strings.Contains(strings.ToLower(w.App), strings.ToLower(app))) {
			cfg.Target = mac.Target{WindowID: w.ID}
			cfg.Width, cfg.Height = area.Pixels(w.Frame.W, w.Frame.H, scaleAt(content, w.Frame), area.MaxW, area.MaxH)
			fmt.Printf("window %d: %s\n", w.ID, w.App)
			return nil
		}
	}
	return errors.New("no such window")
}

// scaleAt is the scale of the display a rect's middle is on.
func scaleAt(content mac.Content, r mac.Rect) float64 {
	mx, my := r.X+r.W/2, r.Y+r.H/2
	for _, d := range content.Displays {
		f := d.Frame
		if mx >= f.X && mx < f.X+f.W && my >= f.Y && my < f.Y+f.H {
			return d.Scale
		}
	}
	if len(content.Displays) > 0 {
		return content.Displays[0].Scale
	}
	return 1
}

// probeSelf opens the beacon window and probes it. The window loop runs
// on the main thread; the stream on a goroutine that quits it at the end.
func probeSelf(cfg mac.StreamConfig, dur, clicksFor time.Duration) error {
	a, err := openWindow(gfx.WindowConfig{Title: "Reel self-test", Width: 480, Height: 300, Hidden: true}, false)
	if err != nil {
		return err
	}
	defer a.close()
	if err := platform.InitAudio(); err != nil {
		return err
	}
	b := newBeacon(500*time.Millisecond, clicksFor)
	defer b.close()
	a.setScene(b)
	// A process started from a terminal doesn't come to the front, and a
	// covered window is never drawn: keep the beacon above everything.
	a.window.Handle().SetAlwaysOnTop(true)
	a.window.Handle().Show()
	a.window.Handle().Raise()
	a.render(a.main)
	id, err := platform.WindowNumber(a.window.Handle())
	if err != nil {
		return err
	}
	slog.Info("probe: window", "id", id, "state", platform.WindowState(a.window.Handle()))
	b.after = func() { slog.Info("probe: window after 1s", "state", platform.WindowState(a.window.Handle())) }
	ww, wh, err := platform.WindowFrameSize(a.window.Handle())
	if err != nil {
		return err
	}
	cfg.Target = mac.Target{WindowID: id}
	cfg.Width, cfg.Height = area.Pixels(ww, wh, float64(a.window.Scale()), area.MaxW, area.MaxH)
	if err := b.begin(); err != nil {
		return err
	}

	errc := make(chan error, 1)
	go func() {
		err := probe(cfg, dur, os.Stdout)
		sdl.PushEvent(&sdl.Event{Type: sdl.EVENT_QUIT})
		errc <- err
	}()
	if err := a.run(0); err != nil {
		return err
	}
	return <-errc
}

// probe runs the stream for dur and writes the report.
func probe(cfg mac.StreamConfig, dur time.Duration, out io.Writer) error {
	p := newProber()
	stopped := make(chan error, 1)
	s, err := mac.StartStream(cfg, p.sample, func(err error) { stopped <- err })
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "recording %d×%d for %s (system audio %v, mic %q)\n", cfg.Width, cfg.Height, dur, cfg.SystemAudio, cfg.Mic)
	var byItself error
	select {
	case <-time.After(dur):
	case byItself = <-stopped:
	}
	clockDiff := math.NaN()
	if c := s.SyncClock(); c != 0 {
		clockDiff = (mac.ClockTime(c).Seconds() - mac.HostTime().Seconds()) * 1000
	}
	if byItself == nil {
		if err := s.Stop(); err != nil {
			fmt.Fprintf(out, "stop: %v\n", err)
		}
	} else {
		fmt.Fprintf(out, "the stream stopped by itself: %v\n", byItself)
	}
	p.report(out, clockDiff)
	return nil
}

// prober collects what each kind of sample did.
type prober struct {
	mu    sync.Mutex
	t0    float64 // the first sample's PTS, seconds
	kinds map[mac.Kind]*kindStats
}

type kindStats struct {
	n            int
	statuses     map[mac.FrameStatus]int
	order        []mac.FrameStatus // the first few statuses, in order
	noImage      int
	size         string
	format       mac.ASBD
	formatSet    bool
	formatChange int
	frames       int64
	first, end   float64 // PTS of the first sample, end of the last (audio)
	lags         []float64
	expect       float64 // the PTS the next audio sample should have
	gaps         int
	maxGap       float64
	perSec       map[int]*secStats
	pcmErr       string // the first PCM read that failed
}

type secStats struct {
	n    int
	peak float64 // linear
}

func newProber() *prober {
	return &prober{t0: math.NaN(), kinds: map[mac.Kind]*kindStats{}}
}

// sample runs on the stream's queue.
func (p *prober) sample(s mac.Sample) {
	now := mac.HostTime().Seconds()
	pts := s.PTS().Seconds()
	p.mu.Lock()
	defer p.mu.Unlock()
	k := p.kinds[s.Kind]
	if k == nil {
		k = &kindStats{statuses: map[mac.FrameStatus]int{}, perSec: map[int]*secStats{}, first: pts}
		p.kinds[s.Kind] = k
	}
	k.n++
	if math.IsNaN(p.t0) && !math.IsNaN(pts) {
		p.t0 = pts
	}
	if !math.IsNaN(pts) {
		k.lags = append(k.lags, (now-pts)*1000)
	}
	sec := k.perSec[int(math.Floor(pts-p.t0))]
	if sec == nil {
		sec = &secStats{}
		k.perSec[int(math.Floor(pts-p.t0))] = sec
	}
	sec.n++

	switch s.Kind {
	case mac.KindScreen:
		st, ok := s.FrameStatus()
		if !ok {
			st = -1
		}
		k.statuses[st]++
		if len(k.order) < 6 {
			k.order = append(k.order, st)
		}
		if w, h, f, ok := s.Image(); ok {
			k.size = fmt.Sprintf("%d×%d %s", w, h, f)
		} else {
			k.noImage++
		}
	default:
		frames := s.Frames()
		k.frames += int64(frames)
		err := s.PCM(func(f mac.ASBD, bufs [][]byte) {
			if !k.formatSet {
				k.format, k.formatSet = f, true
			} else if f != k.format {
				k.formatChange++
				k.format = f
			}
			sec.peak = math.Max(sec.peak, peak(f, bufs))
		})
		if err != nil && k.pcmErr == "" {
			k.pcmErr = err.Error()
		}
		if k.format.SampleRate > 0 && !math.IsNaN(pts) {
			if k.expect != 0 {
				gap := math.Abs(pts-k.expect) * 1000
				k.maxGap = math.Max(k.maxGap, gap)
				if gap > 2 {
					k.gaps++
				}
			}
			k.expect = pts + float64(frames)/k.format.SampleRate
			k.end = k.expect
		}
	}
}

// peak is the largest sample magnitude, 0..1.
func peak(f mac.ASBD, bufs [][]byte) float64 {
	var m float64
	for _, b := range bufs {
		switch {
		case f.Float() && f.BitsPerChannel == 32:
			for i := 0; i+4 <= len(b); i += 4 {
				v := math.Float32frombits(uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24)
				m = math.Max(m, math.Abs(float64(v)))
			}
		case !f.Float() && f.BitsPerChannel == 16:
			for i := 0; i+2 <= len(b); i += 2 {
				v := int16(uint16(b[i]) | uint16(b[i+1])<<8)
				m = math.Max(m, math.Abs(float64(v))/32768)
			}
		case !f.Float() && f.BitsPerChannel == 32:
			for i := 0; i+4 <= len(b); i += 4 {
				v := int32(uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24)
				m = math.Max(m, math.Abs(float64(v))/2147483648)
			}
		}
	}
	return m
}

func dbfs(v float64) string {
	if v <= 0 {
		return "  -inf"
	}
	return fmt.Sprintf("%6.1f", 20*math.Log10(v))
}

func (p *prober) report(out io.Writer, clockDiff float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	kinds := []mac.Kind{mac.KindScreen, mac.KindAudio, mac.KindMic}
	for _, kind := range kinds {
		k := p.kinds[kind]
		if k == nil {
			fmt.Fprintf(out, "%-6s none\n", kind)
			continue
		}
		fmt.Fprintf(out, "%-6s %d samples, lag (host now − PTS) %s\n", kind, k.n, spread(k.lags))
		if kind == mac.KindScreen {
			var order []string
			for _, st := range k.order {
				order = append(order, st.String())
			}
			var counts []string
			for st, n := range k.statuses {
				counts = append(counts, fmt.Sprintf("%s %d", st, n))
			}
			sort.Strings(counts)
			fmt.Fprintf(out, "       statuses: %s; first: %s\n", strings.Join(counts, ", "), strings.Join(order, " → "))
			fmt.Fprintf(out, "       frames: %s; without a pixel buffer: %d\n", k.size, k.noImage)
			continue
		}
		rate := math.NaN()
		if k.end > k.first {
			rate = float64(k.frames) / (k.end - k.first)
		}
		fmt.Fprintf(out, "       format: %s (changed %d times); %.0f frames a sample; measured rate %.1f Hz\n",
			k.format, k.formatChange, float64(k.frames)/float64(k.n), rate)
		fmt.Fprintf(out, "       PTS gaps over 2 ms: %d (largest %.2f ms)\n", k.gaps, k.maxGap)
		if k.pcmErr != "" {
			fmt.Fprintf(out, "       couldn't read the PCM: %s\n", k.pcmErr)
		}
	}
	if !math.IsNaN(clockDiff) {
		fmt.Fprintf(out, "sync clock − host clock: %.3f ms\n", clockDiff)
	} else {
		fmt.Fprintf(out, "sync clock: none\n")
	}

	last := 0
	for _, k := range p.kinds {
		for s := range k.perSec {
			last = max(last, s)
		}
	}
	fmt.Fprintf(out, "\nper second  screen  audio  (peak dBFS)  mic  (peak dBFS)\n")
	for s := 0; s <= last; s++ {
		line := fmt.Sprintf("%9d", s)
		for _, kind := range kinds {
			k := p.kinds[kind]
			st := &secStats{}
			if k != nil && k.perSec[s] != nil {
				st = k.perSec[s]
			}
			if kind == mac.KindScreen {
				line += fmt.Sprintf("  %6d", st.n)
				continue
			}
			line += fmt.Sprintf("  %5d  (%s)", st.n, dbfs(st.peak))
		}
		fmt.Fprintln(out, line)
	}
}

// spread summarizes a list of milliseconds.
func spread(ms []float64) string {
	if len(ms) == 0 {
		return "n/a"
	}
	s := slices.Clone(ms)
	slices.Sort(s)
	q := func(f float64) float64 { return s[min(len(s)-1, int(f*float64(len(s))))] }
	return fmt.Sprintf("min %.1f, median %.1f, p95 %.1f, max %.1f ms", s[0], q(0.5), q(0.95), s[len(s)-1])
}
