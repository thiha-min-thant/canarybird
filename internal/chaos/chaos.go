// Package chaos holds the runtime-adjustable failure modes for this pod.
// Everything here is changed at runtime through PUT /control — never by
// rebuilding the image — so a canary can be broken (and healed) mid-rollout.
package chaos

import (
	"context"
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

// Settings is the complete failure profile of one pod.
type Settings struct {
	Scenario    string  `json:"scenario"`     // preset name, or "custom"
	ErrorRate   float64 `json:"error_rate"`   // 0..1 share of /api/hit requests that fail
	ErrorStatus int     `json:"error_status"` // HTTP status used for failures
	LatencyMs   int     `json:"latency_ms"`   // fixed delay added to every request
	JitterMs    int     `json:"jitter_ms"`    // extra random delay in [0, JitterMs]
	CPUBurnMs   int     `json:"cpu_burn_ms"`  // busy-loop per request
	MemHoldMB   int     `json:"mem_hold_mb"`  // memory held resident by the process
	Ready       bool    `json:"ready"`        // /readyz answers 200 when true
	PanicEvery  int     `json:"panic_every"`  // crash the process every N hits (0 = never)
}

// Healthy is the default profile.
var Healthy = Settings{Scenario: "healthy", ErrorStatus: 500, Ready: true}

// Scenarios are one-click presets. Names are stable: they appear in the UI,
// the API and the README.
var Scenarios = map[string]Settings{
	"healthy":     Healthy,
	"flaky":       {Scenario: "flaky", ErrorRate: 0.10, ErrorStatus: 500, JitterMs: 300, Ready: true},
	"slow-burn":   {Scenario: "slow-burn", ErrorStatus: 500, LatencyMs: 800, JitterMs: 400, Ready: true},
	"error-spike": {Scenario: "error-spike", ErrorRate: 0.50, ErrorStatus: 500, Ready: true},
	"cpu-burn":    {Scenario: "cpu-burn", ErrorStatus: 500, CPUBurnMs: 40, Ready: true},
	"memory-leak": {Scenario: "memory-leak", ErrorStatus: 500, MemHoldMB: 256, Ready: true},
	"not-ready":   {Scenario: "not-ready", ErrorStatus: 500, Ready: false},
	"crash-loop":  {Scenario: "crash-loop", ErrorStatus: 500, Ready: true, PanicEvery: 200},
}

// ScenarioNames returns preset names in a stable, demo-friendly order.
func ScenarioNames() []string {
	order := []string{"healthy", "flaky", "slow-burn", "error-spike", "cpu-burn", "memory-leak", "not-ready", "crash-loop"}
	seen := map[string]bool{}
	out := make([]string, 0, len(Scenarios))
	for _, n := range order {
		if _, ok := Scenarios[n]; ok {
			out = append(out, n)
			seen[n] = true
		}
	}
	rest := []string{}
	for n := range Scenarios {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Controller owns the live settings for this process.
type Controller struct {
	mu       sync.RWMutex
	s        Settings
	mem      [][]byte
	hits     uint64
	onChange []func(Settings)
}

func New() *Controller { return &Controller{s: Healthy} }

func (c *Controller) Get() Settings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.s
}

// OnChange registers a callback invoked after every Set.
func (c *Controller) OnChange(fn func(Settings)) {
	c.mu.Lock()
	c.onChange = append(c.onChange, fn)
	c.mu.Unlock()
}

// Set replaces the profile, clamps it to sane bounds and (re)allocates held memory.
func (c *Controller) Set(s Settings) Settings {
	s = clamp(s)
	c.mu.Lock()
	c.s = s
	c.holdMemoryLocked(s.MemHoldMB)
	fns := append([]func(Settings){}, c.onChange...)
	c.mu.Unlock()
	for _, fn := range fns {
		fn(s)
	}
	return s
}

func clamp(s Settings) Settings {
	s.ErrorRate = min(max(s.ErrorRate, 0), 1)
	if s.ErrorStatus < 400 || s.ErrorStatus > 599 {
		s.ErrorStatus = 500
	}
	s.LatencyMs = min(max(s.LatencyMs, 0), 30_000)
	s.JitterMs = min(max(s.JitterMs, 0), 30_000)
	s.CPUBurnMs = min(max(s.CPUBurnMs, 0), 5_000)
	s.MemHoldMB = min(max(s.MemHoldMB, 0), 4_096)
	s.PanicEvery = max(s.PanicEvery, 0)
	if s.Scenario == "" {
		s.Scenario = "custom"
	}
	return s
}

// holdMemoryLocked keeps mb megabytes resident. Pages are touched so the
// kernel actually commits them — otherwise the demo shows nothing in kubectl top.
func (c *Controller) holdMemoryLocked(mb int) {
	const chunk = 1 << 20
	for len(c.mem) > mb {
		c.mem = c.mem[:len(c.mem)-1]
	}
	for len(c.mem) < mb {
		b := make([]byte, chunk)
		for i := 0; i < chunk; i += 4096 {
			b[i] = byte(i & 0xff) // touch each page so the memory is really resident
		}
		c.mem = append(c.mem, b)
	}
}

// Outcome is what a single /api/hit should do.
type Outcome struct {
	Status int
	Delay  time.Duration
}

// Decide applies the current profile to one request. It blocks for the
// configured delay and CPU burn, honouring ctx cancellation for the sleep.
func (c *Controller) Decide(ctx context.Context) Outcome {
	s := c.Get()

	c.mu.Lock()
	c.hits++
	hits := c.hits
	c.mu.Unlock()
	if s.PanicEvery > 0 && hits%uint64(s.PanicEvery) == 0 {
		// net/http recovers panics inside handlers, so panic from a fresh
		// goroutine: that takes the whole process down, which is the point.
		go func() { panic("canarybird: crash-loop scenario triggered an intentional panic") }()
		time.Sleep(time.Second)
	}

	delay := time.Duration(s.LatencyMs) * time.Millisecond
	if s.JitterMs > 0 {
		delay += time.Duration(rand.IntN(s.JitterMs+1)) * time.Millisecond // #nosec G404
	}
	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
		}
	}
	if s.CPUBurnMs > 0 {
		burn(time.Duration(s.CPUBurnMs) * time.Millisecond)
	}
	status := 200
	if s.ErrorRate > 0 && rand.Float64() < s.ErrorRate { // #nosec G404
		status = s.ErrorStatus
	}
	return Outcome{Status: status, Delay: delay}
}

func burn(d time.Duration) {
	end := time.Now().Add(d)
	x := 0.0
	for time.Now().Before(end) {
		for i := 0; i < 10_000; i++ {
			x += float64(i) * 1.000001
		}
	}
	_ = x
}
