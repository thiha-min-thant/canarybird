package server

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hit is one request as observed by the load generator (the client side),
// which is the only vantage point that sees the real traffic split.
type Hit struct {
	TS        int64   `json:"ts"`
	Version   string  `json:"version"`
	Color     string  `json:"color"`
	Pod       string  `json:"pod"`
	Status    int     `json:"status"`
	LatencyMs float64 `json:"latency_ms"`
	Err       string  `json:"err,omitempty"`
}

// VersionStat is one row of the live table.
type VersionStat struct {
	Version   string  `json:"version"`
	Color     string  `json:"color"`
	Hits      int     `json:"hits"`
	Share     float64 `json:"share_pct"`    // observed share, 0..100
	Expected  float64 `json:"expected_pct"` // user-entered expectation, 0..100, -1 if unset
	Delta     float64 `json:"delta_pct"`    // Share - Expected
	ErrorRate float64 `json:"error_pct"`    // 0..100
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	Pods      int     `json:"pods"`
}

// Stats keeps a sliding window of hits and derives the numbers on screen.
type Stats struct {
	mu       sync.Mutex
	window   []Hit
	size     int
	expected map[string]float64
	rawExp   string
}

func NewStats(size int) *Stats {
	return &Stats{size: size, expected: map[string]float64{}}
}

func (s *Stats) Record(h Hit) {
	s.mu.Lock()
	s.window = append(s.window, h)
	if len(s.window) > s.size {
		s.window = s.window[len(s.window)-s.size:]
	}
	s.mu.Unlock()
}

func (s *Stats) Reset() {
	s.mu.Lock()
	s.window = s.window[:0]
	s.mu.Unlock()
}

// SetExpected parses "v1=90,v2=10" style input. Unknown input is ignored.
func (s *Stats) SetExpected(raw string) {
	exp := map[string]float64{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			k, v, ok = strings.Cut(part, ":")
		}
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "%"), 64)
		if err != nil {
			continue
		}
		exp[strings.TrimSpace(k)] = f
	}
	s.mu.Lock()
	s.expected = exp
	s.rawExp = raw
	s.mu.Unlock()
}

func (s *Stats) Expected() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rawExp
}

// Snapshot computes per-version rows over the current window.
func (s *Stats) Snapshot() (rows []VersionStat, total int, windowSecs float64) {
	s.mu.Lock()
	window := append([]Hit(nil), s.window...)
	expected := s.expected
	s.mu.Unlock()

	total = len(window)
	if total == 0 {
		return nil, 0, 0
	}
	windowSecs = float64(window[len(window)-1].TS-window[0].TS) / 1000

	type acc struct {
		color string
		hits  int
		errs  int
		lat   []float64
		pods  map[string]struct{}
	}
	byV := map[string]*acc{}
	for _, h := range window {
		v := h.Version
		if v == "" {
			v = "unknown"
		}
		a := byV[v]
		if a == nil {
			a = &acc{color: h.Color, pods: map[string]struct{}{}}
			byV[v] = a
		}
		a.hits++
		if h.Status >= 500 || h.Err != "" {
			a.errs++
		}
		if h.Err == "" {
			a.lat = append(a.lat, h.LatencyMs)
		}
		if h.Pod != "" {
			a.pods[h.Pod] = struct{}{}
		}
		if a.color == "" && h.Color != "" {
			a.color = h.Color
		}
	}
	for v, a := range byV {
		sort.Float64s(a.lat)
		row := VersionStat{
			Version:   v,
			Color:     a.color,
			Hits:      a.hits,
			Share:     100 * float64(a.hits) / float64(total),
			Expected:  -1,
			ErrorRate: 100 * float64(a.errs) / float64(a.hits),
			P50Ms:     percentile(a.lat, 0.50),
			P95Ms:     percentile(a.lat, 0.95),
			Pods:      len(a.pods),
		}
		if e, ok := expected[v]; ok {
			row.Expected = e
			row.Delta = row.Share - e
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Version < rows[j].Version })
	return rows, total, windowSecs
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

// Versions returns the version names currently in the window.
func (s *Stats) Versions() []string {
	rows, _, _ := s.Snapshot()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Version)
	}
	return out
}

func nowMs() int64 { return time.Now().UnixMilli() }
