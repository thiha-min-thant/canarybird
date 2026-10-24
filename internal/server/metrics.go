package server

import (
	"runtime"
	"strconv"

	"github.com/thiha-min-thant/canarybird/internal/chaos"
	"github.com/thiha-min-thant/canarybird/internal/telemetry"
)

// Metrics are shaped so a rollout controller can gate on them with one query:
//
//	sum(rate(canarybird_http_requests_total{version="v2",status!~"5.."}[1m]))
//	/ sum(rate(canarybird_http_requests_total{version="v2"}[1m]))
type Metrics struct {
	Registry *telemetry.Registry
	id       Identity

	buildInfo   *telemetry.Gauge
	reqTotal    *telemetry.Counter
	reqDuration *telemetry.Histogram
	chaosErr    *telemetry.Gauge
	chaosLat    *telemetry.Gauge
	chaosCPU    *telemetry.Gauge
	chaosMem    *telemetry.Gauge
	ready       *telemetry.Gauge
	clientTotal *telemetry.Counter
	clientDur   *telemetry.Histogram
	sseClients  *telemetry.Gauge
}

func NewMetrics(id Identity, buildVersion string) *Metrics {
	r := telemetry.NewRegistry()
	m := &Metrics{Registry: r, id: id}
	m.buildInfo = r.NewGauge("canarybird_build_info", "Build and identity of this pod.", "version", "color", "pod", "build", "go_version")
	m.reqTotal = r.NewCounter("canarybird_http_requests_total", "Requests served by /api/hit, labelled by outcome.", "version", "color", "status")
	m.reqDuration = r.NewHistogram("canarybird_http_request_duration_seconds", "Server-side latency of /api/hit.", telemetry.DefaultLatencyBuckets, "version")
	m.chaosErr = r.NewGauge("canarybird_chaos_error_rate", "Configured error rate (0..1).", "version")
	m.chaosLat = r.NewGauge("canarybird_chaos_latency_ms", "Configured fixed latency in ms.", "version")
	m.chaosCPU = r.NewGauge("canarybird_chaos_cpu_burn_ms", "Configured CPU burn per request in ms.", "version")
	m.chaosMem = r.NewGauge("canarybird_chaos_mem_hold_mb", "Memory held resident in MB.", "version")
	m.ready = r.NewGauge("canarybird_ready", "1 when /readyz answers 200.", "version")
	m.clientTotal = r.NewCounter("canarybird_loadgen_requests_total", "Requests made by the built-in load generator, labelled by the version that answered.", "target_version", "status")
	m.clientDur = r.NewHistogram("canarybird_loadgen_request_duration_seconds", "Client-side latency as seen by the load generator.", telemetry.DefaultLatencyBuckets, "target_version")
	m.sseClients = r.NewGauge("canarybird_sse_clients", "Browsers currently watching the grid.")

	m.buildInfo.Set(1, id.Version, id.Color, id.Pod, buildVersion, runtime.Version())
	return m
}

func (m *Metrics) ObserveServer(status int, seconds float64) {
	m.reqTotal.Inc(m.id.Version, m.id.Color, strconv.Itoa(status))
	m.reqDuration.Observe(seconds, m.id.Version)
}

func (m *Metrics) ObserveClient(h Hit) {
	status := strconv.Itoa(h.Status)
	if h.Err != "" {
		status = "error"
	}
	m.clientTotal.Inc(h.Version, status)
	if h.Err == "" {
		m.clientDur.Observe(h.LatencyMs/1000, h.Version)
	}
}

func (m *Metrics) SetChaos(s chaos.Settings) {
	v := m.id.Version
	m.chaosErr.Set(s.ErrorRate, v)
	m.chaosLat.Set(float64(s.LatencyMs), v)
	m.chaosCPU.Set(float64(s.CPUBurnMs), v)
	m.chaosMem.Set(float64(s.MemHoldMB), v)
	if s.Ready {
		m.ready.Set(1, v)
	} else {
		m.ready.Set(0, v)
	}
}

func (m *Metrics) SetSSEClients(n int) { m.sseClients.Set(float64(n)) }
