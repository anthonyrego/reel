package timeline

import "testing"

const ms = int64(1_000_000)

func TestMap(t *testing.T) {
	var tl Timeline
	if _, ok := tl.Map(5 * ms); ok {
		t.Fatal("kept a sample before the start")
	}
	tl.Start(1000 * ms)
	tl.Start(2000 * ms) // only the first start counts
	tl.Pause(4000 * ms)
	tl.Resume(7000 * ms)
	tl.Pause(9000 * ms)
	tl.Resume(9500 * ms)
	for _, c := range []struct {
		pts  int64
		want int64
		keep bool
	}{
		{999 * ms, 0, false}, // before the start
		{1000 * ms, 0, true}, // the first frame
		{3999 * ms, 2999 * ms, true},
		{4000 * ms, 0, false}, // inside the first pause
		{6999 * ms, 0, false},
		{7000 * ms, 3000 * ms, true}, // resumed: 3 s of pause left out
		{9200 * ms, 0, false},
		{9500 * ms, 5000 * ms, true}, // and 0.5 s more
		{12000 * ms, 7500 * ms, true},
	} {
		got, keep := tl.Map(c.pts)
		if keep != c.keep || (keep && got != c.want) {
			t.Errorf("Map(%d ms) = %d ms, %v; want %d ms, %v", c.pts/ms, got/ms, keep, c.want/ms, c.keep)
		}
	}
	if d := tl.PausedFor(20000 * ms); d != 3500*ms {
		t.Errorf("PausedFor = %d ms", d/ms)
	}
}

// A sample captured before Pause but delivered after it is kept: Map only
// looks at the PTS.
func TestLateSampleBeforePause(t *testing.T) {
	var tl Timeline
	tl.Start(0)
	tl.Pause(1000 * ms)
	if got, ok := tl.Map(980 * ms); !ok || got != 980*ms {
		t.Errorf("late sample: %d ms, %v", got/ms, ok)
	}
	if _, ok := tl.Map(1500 * ms); ok {
		t.Error("kept a sample inside the open pause")
	}
	if !tl.Paused() {
		t.Error("not paused")
	}
}

func TestAt(t *testing.T) {
	var tl Timeline
	tl.Start(1000 * ms)
	tl.Pause(3000 * ms)
	// Stopping while paused ends the recording where the pause began.
	if got := tl.At(5000 * ms); got != 2000*ms {
		t.Errorf("At inside the pause = %d ms", got/ms)
	}
	tl.Resume(6000 * ms)
	if got := tl.At(8000 * ms); got != 4000*ms {
		t.Errorf("At after resume = %d ms", got/ms)
	}
	if got := tl.At(10 * ms); got != 0 {
		t.Errorf("At before the start = %d ms", got/ms)
	}
	if got := tl.PausedFor(7000 * ms); got != 3000*ms {
		t.Errorf("PausedFor = %d ms", got/ms)
	}
}

func TestPauseEdges(t *testing.T) {
	var tl Timeline
	tl.Start(0)
	tl.Pause(1000 * ms)
	tl.Pause(1200 * ms) // already paused
	tl.Resume(2000 * ms)
	tl.Resume(2500 * ms) // not paused
	tl.Pause(1500 * ms)  // before the last pause ended: ignored
	if tl.Paused() {
		t.Error("a pause back in time opened")
	}
	if got, _ := tl.Map(3000 * ms); got != 2000*ms {
		t.Errorf("Map = %d ms", got/ms)
	}
	tl.Pause(4000 * ms)
	tl.Resume(3000 * ms) // resumed before it paused: an empty pause
	if got, _ := tl.Map(5000 * ms); got != 4000*ms {
		t.Errorf("after an empty pause Map = %d ms", got/ms)
	}
}
