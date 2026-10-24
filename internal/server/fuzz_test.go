package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/thiha-min-thant/canarybird/internal/chaos"
)

// FuzzSetExpected feeds arbitrary "Expected split" input to the stats parser.
// Garbage must be ignored, never crash, and the raw text must round-trip.
func FuzzSetExpected(f *testing.F) {
	f.Add("v1=90,v2=10")
	f.Add("v1:75%; v2:25%")
	f.Add("=,:,v=,=1,1e400=3,v1=NaN")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		s := NewStats(16)
		s.SetExpected(raw)
		if s.Expected() != raw {
			t.Fatalf("Expected() = %q, want the raw input %q", s.Expected(), raw)
		}
		s.Record(Hit{TS: 1, Version: "v1", Status: 200, LatencyMs: 3})
		s.Record(Hit{TS: 2, Version: "v2", Status: 500, LatencyMs: 9})
		rows, total, _ := s.Snapshot()
		if total != 2 || len(rows) != 2 {
			t.Fatalf("snapshot: %d rows, %d hits", len(rows), total)
		}
		for _, r := range rows {
			if r.Expected != -1 && r.Delta != r.Share-r.Expected {
				t.Fatalf("delta mismatch for %s: %+v", r.Version, r)
			}
		}
	})
}

// FuzzResolveColor checks that any COLOR / VERSION pair yields a usable colour.
func FuzzResolveColor(f *testing.F) {
	f.Add("green", "v2")
	f.Add("", "v1")
	f.Add(" #ABCDEF ", "")
	f.Fuzz(func(t *testing.T, color, version string) {
		got := ResolveColor(color, version)
		if got == "" {
			t.Fatalf("empty colour for (%q, %q)", color, version)
		}
		if got != ResolveColor(color, version) {
			t.Fatal("ResolveColor is not deterministic")
		}
		if strings.TrimSpace(color) == "" {
			// Derived colours must come from the palette.
			known := false
			for _, p := range Palette {
				known = known || p.Hex == got
			}
			if !known {
				t.Fatalf("derived colour %q is not in the palette", got)
			}
		}
	})
}

// FuzzSettingsFromValues drives the /control form parser with arbitrary field
// values and then applies the result exactly as the handler would. Whatever
// comes in, the stored profile must be a preset or "custom" and must encode
// to JSON (the handler's response body).
func FuzzSettingsFromValues(f *testing.F) {
	f.Add("error-spike", "", "", "", "", "", "", "", "", "")
	f.Add("", "50", "", "503", "100", "20", "5", "1", "0", "on")
	f.Add("nope", "x", "nan", "-1", "99999999999", "-7", "", "", "", "")
	f.Add("", "", "1e400", "", "", "", "", "", "", "")
	f.Fuzz(func(t *testing.T, scenario, errPct, errRate, errStatus, latency, jitter, cpu, mem, panicEvery, ready string) {
		v := url.Values{}
		for k, val := range map[string]string{
			"scenario": scenario, "error_pct": errPct, "error_rate": errRate, "error_status": errStatus,
			"latency_ms": latency, "jitter_ms": jitter, "cpu_burn_ms": cpu, "mem_hold_mb": mem,
			"panic_every": panicEvery, "ready": ready,
		} {
			if val != "" {
				v.Set(k, val)
			}
		}
		st := settingsFromValues(v, chaos.Healthy)
		if preset, ok := chaos.Scenarios[scenario]; ok && scenario != "" {
			if st != preset {
				t.Fatalf("known scenario %q not applied: %+v", scenario, st)
			}
		} else if st.Scenario != "custom" {
			t.Fatalf("unknown scenario %q should yield custom, got %q", scenario, st.Scenario)
		}

		// Set() would really allocate mem_hold_mb; the bound itself is covered by chaos.FuzzClamp.
		st.MemHoldMB = 0
		applied := chaos.New().Set(st)
		if _, err := json.Marshal(applied); err != nil {
			t.Fatalf("applied settings do not encode: %v (%+v)", err, applied)
		}
	})
}
