package chaos

import (
	"context"
	"testing"
	"time"
)

func TestClamp(t *testing.T) {
	// clamp() directly: Set would allocate the clamped 4 GiB for real.
	got := clamp(Settings{ErrorRate: 7, ErrorStatus: 42, LatencyMs: -5, MemHoldMB: 1 << 20})
	if got.ErrorRate != 1 || got.ErrorStatus != 500 || got.LatencyMs != 0 || got.MemHoldMB != 4096 || got.Scenario != "custom" {
		t.Fatalf("clamp: %+v", got)
	}
}

func TestDecideHonoursLatency(t *testing.T) {
	c := New()
	c.Set(Settings{LatencyMs: 60, ErrorStatus: 500, Ready: true})
	start := time.Now()
	out := c.Decide(context.Background())
	if out.Status != 200 || time.Since(start) < 55*time.Millisecond {
		t.Fatalf("outcome %+v after %s", out, time.Since(start))
	}
}

func TestDecideCancels(t *testing.T) {
	c := New()
	c.Set(Settings{LatencyMs: 5000, ErrorStatus: 500, Ready: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	c.Decide(ctx)
	if time.Since(start) > time.Second {
		t.Fatal("sleep ignored context cancellation")
	}
}

func TestMemoryHold(t *testing.T) {
	c := New()
	c.Set(Settings{MemHoldMB: 8, ErrorStatus: 500, Ready: true})
	if len(c.mem) != 8 {
		t.Fatalf("held %d MB", len(c.mem))
	}
	c.Set(Settings{MemHoldMB: 2, ErrorStatus: 500, Ready: true})
	if len(c.mem) != 2 {
		t.Fatalf("held %d MB after shrink", len(c.mem))
	}
}

func TestScenarioNamesStable(t *testing.T) {
	names := ScenarioNames()
	if names[0] != "healthy" || len(names) != len(Scenarios) {
		t.Fatalf("names %v", names)
	}
}

// FuzzClamp checks that any Settings a client can send is forced into the
// documented bounds and that clamping is idempotent.
func FuzzClamp(f *testing.F) {
	f.Add("flaky", 0.10, 500, 0, 300, 0, 0, 0, true)
	f.Add("", 7.0, 42, -5, -1, 99_999, 1<<20, -3, false)
	f.Add("custom", -0.5, 599, 30_001, 30_001, 5_001, 4_097, 1, true)
	f.Fuzz(func(t *testing.T, scenario string, errRate float64, status, latency, jitter, cpu, mem, panicEvery int, ready bool) {
		in := Settings{
			Scenario: scenario, ErrorRate: errRate, ErrorStatus: status,
			LatencyMs: latency, JitterMs: jitter, CPUBurnMs: cpu, MemHoldMB: mem,
			Ready: ready, PanicEvery: panicEvery,
		}
		got := clamp(in)
		if !(got.ErrorRate >= 0 && got.ErrorRate <= 1) { // also rejects NaN
			t.Fatalf("error rate %v out of [0,1] for input %v", got.ErrorRate, errRate)
		}
		if got.ErrorStatus < 400 || got.ErrorStatus > 599 {
			t.Fatalf("error status %d outside 4xx/5xx", got.ErrorStatus)
		}
		if got.LatencyMs < 0 || got.LatencyMs > 30_000 || got.JitterMs < 0 || got.JitterMs > 30_000 {
			t.Fatalf("latency %d / jitter %d outside [0,30000]", got.LatencyMs, got.JitterMs)
		}
		if got.CPUBurnMs < 0 || got.CPUBurnMs > 5_000 {
			t.Fatalf("cpu burn %d outside [0,5000]", got.CPUBurnMs)
		}
		if got.MemHoldMB < 0 || got.MemHoldMB > 4_096 {
			t.Fatalf("mem hold %d outside [0,4096]", got.MemHoldMB)
		}
		if got.PanicEvery < 0 {
			t.Fatalf("panic every %d negative", got.PanicEvery)
		}
		if got.Scenario == "" {
			t.Fatal("scenario must never be empty after clamp")
		}
		if got.Ready != ready {
			t.Fatal("clamp must not touch Ready")
		}
		if again := clamp(got); again != got {
			t.Fatalf("clamp not idempotent: %+v then %+v", got, again)
		}
	})
}
