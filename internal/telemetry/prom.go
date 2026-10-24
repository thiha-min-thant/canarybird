// Package telemetry is a tiny, dependency-free Prometheus exposition
// implementation. It supports counters, gauges and histograms with labels,
// which is everything canarybird needs. It exists so the binary stays
// stdlib-only and the image stays a few megabytes.
package telemetry

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry holds metrics and renders them in the Prometheus text format.
type Registry struct {
	mu   sync.Mutex
	list []metric
}

type metric interface {
	write(w io.Writer)
}

func NewRegistry() *Registry { return &Registry{} }

// Handler serves /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, m := range r.list {
			m.write(w)
		}
	})
}

func (r *Registry) add(m metric) {
	r.mu.Lock()
	r.list = append(r.list, m)
	r.mu.Unlock()
}

// ---- label handling -------------------------------------------------------

type series[T any] struct {
	mu     sync.Mutex
	names  []string
	values map[string]*entry[T]
}

type entry[T any] struct {
	labels []string
	v      T
}

func newSeries[T any](names []string) *series[T] {
	return &series[T]{names: names, values: map[string]*entry[T]{}}
}

func (s *series[T]) get(labels []string, mk func() T) *entry[T] {
	if len(labels) != len(s.names) {
		panic(fmt.Sprintf("telemetry: expected %d labels, got %d", len(s.names), len(labels)))
	}
	key := strings.Join(labels, "\x00")
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.values[key]
	if !ok {
		e = &entry[T]{labels: append([]string(nil), labels...), v: mk()}
		s.values[key] = e
	}
	return e
}

func (s *series[T]) sorted() []*entry[T] {
	s.mu.Lock()
	out := make([]*entry[T], 0, len(s.values))
	for _, e := range s.values {
		out = append(out, e)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i].labels, "\x00") < strings.Join(out[j].labels, "\x00")
	})
	return out
}

func labelString(names, values []string, extra ...string) string {
	if len(names) == 0 && len(extra) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for i, n := range names {
		if !first {
			b.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&b, `%s="%s"`, n, escape(values[i]))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if !first {
			b.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&b, `%s="%s"`, extra[i], extra[i+1])
	}
	b.WriteByte('}')
	return b.String()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// ---- counter --------------------------------------------------------------

type Counter struct {
	name, help string
	s          *series[*atomic.Uint64]
}

func (r *Registry) NewCounter(name, help string, labels ...string) *Counter {
	c := &Counter{name: name, help: help, s: newSeries[*atomic.Uint64](labels)}
	r.add(c)
	return c
}

func (c *Counter) Inc(labels ...string) { c.Add(1, labels...) }

func (c *Counter) Add(n uint64, labels ...string) {
	c.s.get(labels, func() *atomic.Uint64 { return &atomic.Uint64{} }).v.Add(n)
}

func (c *Counter) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
	for _, e := range c.s.sorted() {
		fmt.Fprintf(w, "%s%s %d\n", c.name, labelString(c.s.names, e.labels), e.v.Load())
	}
}

// ---- gauge ----------------------------------------------------------------

type Gauge struct {
	name, help string
	s          *series[*atomic.Uint64] // float64 bits
}

func (r *Registry) NewGauge(name, help string, labels ...string) *Gauge {
	g := &Gauge{name: name, help: help, s: newSeries[*atomic.Uint64](labels)}
	r.add(g)
	return g
}

func (g *Gauge) Set(v float64, labels ...string) {
	g.s.get(labels, func() *atomic.Uint64 { return &atomic.Uint64{} }).v.Store(math.Float64bits(v))
}

func (g *Gauge) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.name, g.help, g.name)
	for _, e := range g.s.sorted() {
		fmt.Fprintf(w, "%s%s %s\n", g.name, labelString(g.s.names, e.labels), fmtFloat(math.Float64frombits(e.v.Load())))
	}
}

// ---- histogram ------------------------------------------------------------

type histData struct {
	mu      sync.Mutex
	buckets []uint64
	sum     float64
	count   uint64
}

type Histogram struct {
	name, help string
	bounds     []float64
	s          *series[*histData]
}

// DefaultLatencyBuckets are tuned for request latencies in seconds.
var DefaultLatencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

func (r *Registry) NewHistogram(name, help string, bounds []float64, labels ...string) *Histogram {
	h := &Histogram{name: name, help: help, bounds: bounds, s: newSeries[*histData](labels)}
	r.add(h)
	return h
}

func (h *Histogram) Observe(v float64, labels ...string) {
	d := h.s.get(labels, func() *histData { return &histData{buckets: make([]uint64, len(h.bounds))} }).v
	d.mu.Lock()
	for i, b := range h.bounds {
		if v <= b {
			d.buckets[i]++
		}
	}
	d.sum += v
	d.count++
	d.mu.Unlock()
}

func (h *Histogram) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
	for _, e := range h.s.sorted() {
		d := e.v
		d.mu.Lock()
		for i, b := range h.bounds {
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelString(h.s.names, e.labels, "le", fmtFloat(b)), d.buckets[i])
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelString(h.s.names, e.labels, "le", "+Inf"), d.count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.name, labelString(h.s.names, e.labels), fmtFloat(d.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, labelString(h.s.names, e.labels), d.count)
		d.mu.Unlock()
	}
}

func fmtFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
