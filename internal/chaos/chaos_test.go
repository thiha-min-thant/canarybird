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
