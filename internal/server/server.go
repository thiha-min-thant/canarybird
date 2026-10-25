// Package server wires identity, chaos, the load generator and the UI into
// one http.Handler. It is deliberately a single binary with an embedded UI:
// no build step, no CDN, works air-gapped.
package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thiha-min-thant/canarybird/internal/chaos"
)

//go:embed all:web
var webFS embed.FS

// Config is read from the environment by cmd/canarybird.
type Config struct {
	Port         string
	Build        string
	ControlPeers string // headless Service DNS name; enables fan-out of /control to every pod
	Loadgen      LoadgenSettings
	Log          *slog.Logger
}

type Server struct {
	cfg      Config
	id       Identity
	chaos    *chaos.Controller
	loadgen  *Loadgen
	stats    *Stats
	broker   *Broker
	metrics  *Metrics
	log      *slog.Logger
	tmpl     *template.Template
	mux      *http.ServeMux
	started  time.Time
	draining atomic.Bool

	mu         sync.Mutex
	lastTarget string
}

func New(cfg Config) (*Server, error) {
	id := IdentityFromEnv()
	funcs := template.FuncMap{
		"pct":    func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) },
		"ms":     func(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) },
		"pct100": func(f float64) int { return int(f * 100) },
		"signed": func(f float64) string {
			if f >= 0 {
				return "+" + strconv.FormatFloat(f, 'f', 1, 64)
			}
			return strconv.FormatFloat(f, 'f', 1, 64)
		},
		"abs": func(f float64) float64 {
			if f < 0 {
				return -f
			}
			return f
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(webFS, "web/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	s := &Server{
		cfg:     cfg,
		id:      id,
		chaos:   chaos.New(),
		stats:   NewStats(600),
		broker:  NewBroker(),
		log:     cfg.Log,
		tmpl:    tmpl,
		started: time.Now(),
	}
	s.metrics = NewMetrics(id, cfg.Build)
	s.metrics.SetChaos(s.chaos.Get())
	s.loadgen = NewLoadgen(cfg.Loadgen, s.stats, s.broker, s.metrics, cfg.Log)
	s.chaos.OnChange(func(st chaos.Settings) {
		s.metrics.SetChaos(st)
		s.log.Info("chaos updated", "scenario", st.Scenario, "error_rate", st.ErrorRate, "latency_ms", st.LatencyMs,
			"jitter_ms", st.JitterMs, "cpu_burn_ms", st.CPUBurnMs, "mem_hold_mb", st.MemHoldMB, "ready", st.Ready)
		s.broker.Publish("control", s.render("control", s.controlView("", "")))
	})
	s.routes()
	return s, nil
}

// Run starts background work and serves until ctx is cancelled, then drains.
func (s *Server) Run(ctx context.Context) error {
	go s.loadgen.Run(ctx)
	go s.statsTicker(ctx)

	srv := &http.Server{
		Addr:              ":" + s.cfg.Port,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.log.Info("canarybird listening", "port", s.cfg.Port, "version", s.id.Version, "color", s.id.Color, "pod", s.id.Pod)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	// Fail readiness first so the endpoint slice drops us before we close.
	s.draining.Store(true)
	delay := envDuration("SHUTDOWN_DELAY", 2*time.Second)
	s.log.Info("draining", "delay", delay)
	time.Sleep(delay)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func (s *Server) routes() {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/hit", s.handleHit)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /events", s.handleEvents)

	mux.HandleFunc("GET /control", s.handleControlGet)
	mux.HandleFunc("PUT /control", s.handleControlSet)
	mux.HandleFunc("POST /control", s.handleControlSet)
	mux.HandleFunc("GET /loadgen", s.handleLoadgenGet)
	mux.HandleFunc("PUT /loadgen", s.handleLoadgenSet)
	mux.HandleFunc("POST /loadgen", s.handleLoadgenSet)
	mux.HandleFunc("POST /stats/reset", s.handleStatsReset)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("GET /metrics", s.metrics.Registry.Handler())
	s.mux = mux
}

// ---- traffic endpoint -----------------------------------------------------

func (s *Server) handleHit(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	out := s.chaos.Decide(r.Context())
	s.identityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(out.Status)
	json.NewEncoder(w).Encode(map[string]any{
		"version": s.id.Version, "color": s.id.Color, "pod": s.id.Pod, "node": s.id.Node, "zone": s.id.Zone,
		"status": out.Status, "delay_ms": out.Delay.Milliseconds(), "ts": nowMs(),
	})
	s.metrics.ObserveServer(out.Status, time.Since(start).Seconds())
	s.log.Debug("hit", "status", out.Status, "delay_ms", out.Delay.Milliseconds(), "remote", r.RemoteAddr)
}

func (s *Server) identityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Canarybird-Version", s.id.Version)
	h.Set("X-Canarybird-Color", s.id.Color)
	h.Set("X-Canarybird-Pod", s.id.Pod)
	if s.id.Node != "" {
		h.Set("X-Canarybird-Node", s.id.Node)
	}
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	s.identityHeaders(w)
	writeJSON(w, s.id)
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	if !s.chaos.Get().Ready {
		http.Error(w, "not ready (chaos)", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ready\n"))
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	rows, total, secs := s.stats.Snapshot()
	if wantsText(r) {
		s.writeStateText(w, r)
		return
	}
	writeJSON(w, map[string]any{
		"identity": s.id, "chaos": s.chaos.Get(), "loadgen": s.loadgen.Get(),
		"stats":          map[string]any{"versions": rows, "window_hits": total, "window_seconds": secs, "expected": s.stats.Expected()},
		"uptime_seconds": int(time.Since(s.started).Seconds()), "build": s.cfg.Build, "sse_clients": s.broker.Count(),
	})
}

// ---- UI -------------------------------------------------------------------

type pageView struct {
	Identity Identity
	Build    string
	Control  controlView
	Loadgen  loadgenView
	Stats    statsView
	Peers    bool
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if wantsText(r) { // `curl canarybird` on a jump host: the dashboard as text
		s.writeStateText(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.identityHeaders(w)
	v := pageView{
		Identity: s.id, Build: s.cfg.Build,
		Control: s.controlView("", ""), Loadgen: s.loadgenView("", ""), Stats: s.statsView(),
		Peers: s.cfg.ControlPeers != "",
	}
	if err := s.tmpl.ExecuteTemplate(w, "index.html", v); err != nil {
		s.log.Error("render index", "err", err)
	}
}

func (s *Server) render(name string, data any) []byte {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render", "template", name, "err", err)
	}
	return buf.Bytes()
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if wantsText(r) { // `curl -N canarybird/events`: the grid, one glyph per request
		s.streamHitsText(w, r, flusher)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	s.identityHeaders(w)

	ch, cancel := s.broker.Subscribe()
	defer cancel()
	s.metrics.SetSSEClients(s.broker.Count())
	defer func() { s.metrics.SetSSEClients(s.broker.Count()) }()

	// Prime the browser with current state so a reconnect looks seamless.
	fmt.Fprintf(w, "retry: 2000\n\n")
	fmt.Fprintf(w, "event: hello\ndata: %s\n\n", mustJSON(s.id))
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-ch:
			w.Write(frame)
			flusher.Flush()
		case <-keepalive.C:
			w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) statsTicker(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.broker.Count() == 0 {
				continue
			}
			s.broker.Publish("stats", s.render("stats", s.statsView()))
		}
	}
}

// ---- stats ----------------------------------------------------------------

type statsView struct {
	Rows       []VersionStat
	Total      int
	WindowSecs float64
	Expected   string
	Enabled    bool
	Pod        string
}

func (s *Server) statsView() statsView {
	rows, total, secs := s.stats.Snapshot()
	return statsView{Rows: rows, Total: total, WindowSecs: secs, Expected: s.stats.Expected(), Enabled: s.loadgen.Get().Enabled, Pod: s.id.Pod}
}

func (s *Server) handleStatsReset(w http.ResponseWriter, r *http.Request) {
	s.stats.Reset()
	if wantsText(r) {
		textHeaders(w)
		fmt.Fprintln(w, "window reset")
		return
	}
	w.Write(s.render("stats", s.statsView()))
}

// ---- loadgen --------------------------------------------------------------

type loadgenView struct {
	Settings   LoadgenSettings
	Expected   string
	Flash      string
	FlashKind  string
	RPSOptions []int
}

func (s *Server) loadgenView(flash, kind string) loadgenView {
	return loadgenView{Settings: s.loadgen.Get(), Expected: s.stats.Expected(), Flash: flash, FlashKind: kind, RPSOptions: []int{5, 10, 20, 50, 100, 200}}
}

func (s *Server) handleLoadgenGet(w http.ResponseWriter, r *http.Request) {
	if wantsText(r) {
		textHeaders(w)
		fmt.Fprintln(w, trafficLine(s.loadgen.Get(), s.stats.Expected()))
		return
	}
	w.Write(s.render("loadgen", s.loadgenView("", "")))
}

func (s *Server) handleLoadgenSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cur := s.loadgen.Get()
	next := LoadgenSettings{
		Enabled:   formBool(r, "enabled", false),
		RPS:       atoiDefault(r.FormValue("rps"), cur.RPS),
		KeepAlive: formBool(r, "keepalive", false),
		Target:    firstNonEmpty(r.FormValue("target"), cur.Target),
	}
	if r.Form.Has("expected") {
		s.stats.SetExpected(r.FormValue("expected"))
	}
	applied := s.loadgen.Set(next)
	msg := "Traffic stopped."
	if applied.Enabled {
		mode := "new connection per request"
		if applied.KeepAlive {
			mode = "keep-alive (connections pin to a pod)"
		}
		msg = fmt.Sprintf("Sending %d req/s to %s — %s.", applied.RPS, applied.Target, mode)
	}
	if wantsJSON(r) {
		writeJSON(w, applied)
		return
	}
	if wantsText(r) {
		textHeaders(w)
		fmt.Fprintln(w, msg)
		return
	}
	w.Write(s.render("loadgen", s.loadgenView(msg, "ok")))
}

// ---- control --------------------------------------------------------------

type controlView struct {
	Settings  chaos.Settings
	Scenarios []string
	Versions  []string
	Target    string
	Pod       string
	Version   string
	Peers     bool
	Flash     string
	FlashKind string
	ErrorPct  int
}

func (s *Server) controlView(flash, kind string) controlView {
	s.mu.Lock()
	target := s.lastTarget
	s.mu.Unlock()
	versions := s.stats.Versions()
	if !contains(versions, s.id.Version) {
		versions = append([]string{s.id.Version}, versions...)
	}
	st := s.chaos.Get()
	return controlView{
		Settings: st, Scenarios: chaos.ScenarioNames(), Versions: versions, Target: target,
		Pod: s.id.Pod, Version: s.id.Version, Peers: s.cfg.ControlPeers != "",
		Flash: flash, FlashKind: kind, ErrorPct: int(st.ErrorRate*100 + 0.5),
	}
}

func (s *Server) handleControlGet(w http.ResponseWriter, r *http.Request) {
	if wantsText(r) {
		textHeaders(w)
		fmt.Fprintln(w, chaosLine(s.chaos.Get()))
		return
	}
	w.Write(s.render("control", s.controlView("", "")))
}

type applyResult struct {
	Pod     string `json:"pod"`
	Version string `json:"version"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// handleControlSet is the heart of the harness. Settings are per pod, so when
// CONTROL_PEERS points at a headless Service we resolve every pod IP and fan
// the request out; each pod applies it only if its VERSION matches the target.
func (s *Server) handleControlSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target := strings.TrimSpace(r.FormValue("target_version"))
	s.mu.Lock()
	s.lastTarget = target
	s.mu.Unlock()
	settings := settingsFromForm(r, s.chaos.Get())
	hop := r.Header.Get("X-Canarybird-Hop") != ""

	// A forwarded request: apply locally if we are the target, answer JSON.
	if hop || s.cfg.ControlPeers == "" {
		res := s.applyLocal(target, settings)
		if hop || wantsJSON(r) {
			writeJSON(w, res)
			return
		}
		msg := fmt.Sprintf("Applied %s to this pod (%s).", settings.Scenario, s.id.Pod)
		kind := "ok"
		if !res.Applied {
			msg = fmt.Sprintf("This pod is %s, not %s — nothing changed. Set CONTROL_PEERS to reach other pods.", s.id.Version, target)
			kind = "warn"
		}
		if wantsText(r) {
			textHeaders(w)
			fmt.Fprintln(w, msg)
			return
		}
		w.Write(s.render("control", s.controlView(msg, kind)))
		return
	}

	results := s.fanOut(r.Context(), r.Form)
	applied, byVersion := 0, map[string]int{}
	errs := 0
	for _, res := range results {
		if res.Error != "" {
			errs++
			continue
		}
		if res.Applied {
			applied++
			byVersion[res.Version]++
		}
	}
	if wantsJSON(r) {
		writeJSON(w, map[string]any{"results": results, "applied": applied})
		return
	}
	var parts []string
	for v, n := range byVersion {
		parts = append(parts, fmt.Sprintf("%s ×%d", v, n))
	}
	msg := fmt.Sprintf("Applied %s to %d of %d pods", settings.Scenario, applied, len(results))
	if len(parts) > 0 {
		msg += " (" + strings.Join(parts, ", ") + ")"
	}
	msg += "."
	kind := "ok"
	if applied == 0 {
		kind = "warn"
		if target != "" {
			msg = fmt.Sprintf("No pod answering as %s among %d reachable pods — nothing changed.", target, len(results))
		}
	}
	if errs > 0 {
		msg += fmt.Sprintf(" %d pod(s) unreachable.", errs)
	}
	if wantsText(r) {
		textHeaders(w)
		fmt.Fprintln(w, msg)
		return
	}
	w.Write(s.render("control", s.controlView(msg, kind)))
}

func (s *Server) applyLocal(target string, settings chaos.Settings) applyResult {
	res := applyResult{Pod: s.id.Pod, Version: s.id.Version}
	if target == "" || target == s.id.Version {
		s.chaos.Set(settings)
		res.Applied = true
	}
	return res
}

func (s *Server) fanOut(ctx context.Context, form url.Values) []applyResult {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, s.cfg.ControlPeers)
	if err != nil || len(ips) == 0 {
		s.log.Warn("control peers lookup failed, applying locally", "peers", s.cfg.ControlPeers, "err", err)
		target := form.Get("target_version")
		return []applyResult{s.applyLocal(target, settingsFromValues(form, s.chaos.Get()))}
	}
	client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	results := make([]applyResult, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func(i int, ip net.IPAddr) {
			defer wg.Done()
			addr := net.JoinHostPort(ip.IP.String(), s.cfg.Port)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://"+addr+"/control", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-Canarybird-Hop", "1")
			resp, err := client.Do(req) // #nosec G704 -- targets are the pods behind the operator-configured CONTROL_PEERS Service, not user input
			if err != nil {
				results[i] = applyResult{Pod: addr, Error: shortErr(err)}
				return
			}
			defer resp.Body.Close()
			var res applyResult
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				results[i] = applyResult{Pod: addr, Error: "bad response"}
				return
			}
			results[i] = res
		}(i, ip)
	}
	wg.Wait()
	return results
}

func settingsFromForm(r *http.Request, cur chaos.Settings) chaos.Settings {
	return settingsFromValues(r.Form, cur)
}

func settingsFromValues(f url.Values, cur chaos.Settings) chaos.Settings {
	if name := f.Get("scenario"); name != "" {
		if preset, ok := chaos.Scenarios[name]; ok {
			return preset
		}
	}
	st := cur
	st.Scenario = "custom"
	if f.Has("error_pct") {
		st.ErrorRate = float64(atoiDefault(f.Get("error_pct"), int(cur.ErrorRate*100))) / 100
	}
	if f.Has("error_rate") {
		if v, err := strconv.ParseFloat(f.Get("error_rate"), 64); err == nil {
			st.ErrorRate = v
		}
	}
	if f.Has("error_status") {
		st.ErrorStatus = atoiDefault(f.Get("error_status"), cur.ErrorStatus)
	}
	if f.Has("latency_ms") {
		st.LatencyMs = atoiDefault(f.Get("latency_ms"), cur.LatencyMs)
	}
	if f.Has("jitter_ms") {
		st.JitterMs = atoiDefault(f.Get("jitter_ms"), cur.JitterMs)
	}
	if f.Has("cpu_burn_ms") {
		st.CPUBurnMs = atoiDefault(f.Get("cpu_burn_ms"), cur.CPUBurnMs)
	}
	if f.Has("mem_hold_mb") {
		st.MemHoldMB = atoiDefault(f.Get("mem_hold_mb"), cur.MemHoldMB)
	}
	if f.Has("panic_every") {
		st.PanicEvery = atoiDefault(f.Get("panic_every"), cur.PanicEvery)
	}
	// Checkboxes: present means on. The form always carries a hidden
	// ready_present=1 so an unticked box reads as false, not "unchanged".
	if f.Has("ready_present") {
		st.Ready = f.Get("ready") != ""
	} else if f.Has("ready") {
		st.Ready = parseBool(f.Get("ready"))
	}
	return st
}

// ---- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") && r.Header.Get("HX-Request") == ""
}

// wantsText decides when a response is for a terminal rather than a browser
// or a program. curl and wget get text by default, so that on a jump host
// `curl canarybird` just works; ?format=json or Accept: application/json
// gets the machine form back. htmx requests are never text.
func wantsText(r *http.Request) bool {
	if r.Header.Get("HX-Request") != "" {
		return false
	}
	switch r.URL.Query().Get("format") {
	case "text", "txt", "plain":
		return true
	case "json", "html", "sse":
		return false
	}
	accept := r.Header.Get("Accept")
	if strings.HasPrefix(accept, "text/plain") {
		return true
	}
	if strings.Contains(accept, "json") || strings.Contains(accept, "html") || strings.Contains(accept, "event-stream") {
		return false
	}
	ua := r.Header.Get("User-Agent")
	return strings.HasPrefix(ua, "curl/") || strings.HasPrefix(ua, "Wget/")
}

func formBool(r *http.Request, key string, def bool) bool {
	if !r.Form.Has(key) && !r.Form.Has(key+"_present") {
		return def
	}
	return parseBool(r.FormValue(key))
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes", "y":
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
