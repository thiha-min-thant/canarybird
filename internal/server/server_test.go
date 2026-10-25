package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("VERSION", "v1")
	t.Setenv("COLOR", "blue")
	t.Setenv("POD_NAME", "test-pod")
	s, err := New(Config{Port: "0", Build: "test", Loadgen: LoadgenSettings{RPS: 10, Target: "http://127.0.0.1:1"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func do(s *Server, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	return rr
}

func TestHitCarriesIdentity(t *testing.T) {
	s := newTestServer(t)
	rr := do(s, "GET", "/api/hit", "")
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if got := rr.Header().Get("X-Canarybird-Version"); got != "v1" {
		t.Fatalf("version header %q", got)
	}
	if got := rr.Header().Get("X-Canarybird-Color"); got != "#4C8DFF" {
		t.Fatalf("color header %q", got)
	}
	if !strings.Contains(rr.Body.String(), `"pod":"test-pod"`) {
		t.Fatalf("body %s", rr.Body.String())
	}
}

func TestScenarioChangesOutcome(t *testing.T) {
	s := newTestServer(t)
	rr := do(s, "PUT", "/control", "scenario=error-spike", "Accept", "application/json")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"applied":true`) {
		t.Fatalf("control: %d %s", rr.Code, rr.Body.String())
	}
	errs := 0
	for i := 0; i < 200; i++ {
		if do(s, "GET", "/api/hit", "").Code == 500 {
			errs++
		}
	}
	if errs < 60 || errs > 140 {
		t.Fatalf("expected ~50%% errors, got %d/200", errs)
	}
	if do(s, "GET", "/readyz", "").Code != 200 {
		t.Fatal("readyz should still be 200")
	}
	do(s, "PUT", "/control", "scenario=not-ready")
	if do(s, "GET", "/readyz", "").Code != 503 {
		t.Fatal("readyz should be 503 after not-ready")
	}
}

func TestTargetVersionIsRespected(t *testing.T) {
	s := newTestServer(t)
	rr := do(s, "PUT", "/control", "scenario=error-spike&target_version=v2", "Accept", "application/json")
	if !strings.Contains(rr.Body.String(), `"applied":false`) {
		t.Fatalf("v2 settings must not apply to a v1 pod: %s", rr.Body.String())
	}
	if s.chaos.Get().Scenario != "healthy" {
		t.Fatal("scenario changed")
	}
}

func TestCustomSliders(t *testing.T) {
	s := newTestServer(t)
	do(s, "PUT", "/control", "error_pct=20&latency_ms=100&jitter_ms=0&ready_present=1")
	st := s.chaos.Get()
	if st.ErrorRate != 0.2 || st.LatencyMs != 100 || st.Ready {
		t.Fatalf("unexpected settings %+v", st)
	}
}

func TestIndexAndFragmentsRender(t *testing.T) {
	s := newTestServer(t)
	for _, p := range []string{"/", "/control", "/loadgen", "/metrics", "/api/state", "/healthz", "/static/app.css", "/static/grid.js", "/static/vendor/htmx.min.js"} {
		if rr := do(s, "GET", p, ""); rr.Code != 200 {
			t.Errorf("%s: %d", p, rr.Code)
		}
	}
	body := do(s, "GET", "/", "").Body.String()
	for _, want := range []string{`sse-connect="/events"`, `id="grid"`, `hx-put="/control"`, `hx-put="/loadgen"`} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %s", want)
		}
	}
}

func TestMetricsExposition(t *testing.T) {
	s := newTestServer(t)
	do(s, "GET", "/api/hit", "")
	body := do(s, "GET", "/metrics", "").Body.String()
	for _, want := range []string{
		`# TYPE canarybird_http_requests_total counter`,
		`canarybird_http_requests_total{version="v1",color="#4C8DFF",status="200"} 1`,
		`canarybird_http_request_duration_seconds_bucket{version="v1",le="+Inf"} 1`,
		`canarybird_ready{version="v1"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestStatsSnapshotAndExpected(t *testing.T) {
	st := NewStats(100)
	for i := 0; i < 90; i++ {
		st.Record(Hit{TS: int64(i), Version: "v1", Color: "#111", Pod: "a", Status: 200, LatencyMs: 10})
	}
	for i := 0; i < 10; i++ {
		st.Record(Hit{TS: int64(100 + i), Version: "v2", Color: "#222", Pod: "b", Status: 500, LatencyMs: 300})
	}
	st.SetExpected("v1=80, v2=20%")
	rows, total, _ := st.Snapshot()
	if total != 100 || len(rows) != 2 {
		t.Fatalf("total %d rows %d", total, len(rows))
	}
	if rows[0].Share != 90 || rows[0].Expected != 80 || rows[0].Delta != 10 {
		t.Fatalf("v1 row %+v", rows[0])
	}
	if rows[1].ErrorRate != 100 || rows[1].Pods != 1 || rows[1].P95Ms != 300 {
		t.Fatalf("v2 row %+v", rows[1])
	}
}

func TestSSEStreamsHello(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "event: hello") {
		t.Fatalf("first frame: %q", buf[:n])
	}
}

func TestCurlGetsText(t *testing.T) {
	s := newTestServer(t)
	s.stats.SetExpected("v1=90,v2=10")
	s.stats.Record(Hit{TS: 1000, Version: "v1", Color: "#4C8DFF", Pod: "a", Status: 200, LatencyMs: 2})
	s.stats.Record(Hit{TS: 2000, Version: "v2", Color: "#3DD68C", Pod: "b", Status: 500, LatencyMs: 900})
	cases := []struct {
		name, path string
		headers    []string
	}{
		{"curl user agent", "/api/state", []string{"User-Agent", "curl/8.7.1"}},
		{"curl on index", "/", []string{"User-Agent", "curl/8.7.1"}},
		{"accept text", "/api/state", []string{"Accept", "text/plain"}},
		{"format=text", "/api/state?format=text&color=none", nil},
	}
	for _, c := range cases {
		rr := do(s, "GET", c.path, "", c.headers...)
		if rr.Code != 200 || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %d %s", c.name, rr.Code, rr.Header().Get("Content-Type"))
		}
		body := rr.Body.String()
		for _, want := range []string{"pod test-pod", "expected: v1=90,v2=10", "VERSION", "v1", "50.0%", "90.0%", "-40.0", "100.0%", "last 2 requests", "█", "│"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q in\n%s", c.name, want, body)
			}
		}
		if colour := strings.Contains(body, "\x1b["); colour == strings.Contains(c.path, "color=none") {
			t.Errorf("%s: colour=%v", c.name, colour)
		}
	}
	// Programs and browsers are untouched.
	notText := []struct {
		path    string
		headers []string
		ctype   string
	}{
		{"/api/state", nil, "application/json"},
		{"/api/state", []string{"User-Agent", "curl/8.7.1", "Accept", "application/json"}, "application/json"},
		{"/api/state?format=json", []string{"User-Agent", "curl/8.7.1"}, "application/json"},
		{"/", nil, "text/html"},
		{"/", []string{"User-Agent", "curl/8.7.1", "Accept", "text/html"}, "text/html"},
	}
	for _, c := range notText {
		rr := do(s, "GET", c.path, "", c.headers...)
		if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, c.ctype) {
			t.Errorf("%s %v: content-type %q, want %s", c.path, c.headers, got, c.ctype)
		}
	}
	// htmx polling from a browser whose UA is spoofed as curl still gets its fragment.
	if rr := do(s, "GET", "/control", "", "User-Agent", "curl/8.7.1", "HX-Request", "true"); !strings.HasPrefix(strings.TrimSpace(rr.Body.String()), "<form") {
		t.Errorf("htmx fragment: %q", rr.Body.String()[:40])
	}
	// Control routes answer in one line.
	rr := do(s, "PUT", "/control", "scenario=error-spike", "User-Agent", "curl/8.7.1")
	if body := rr.Body.String(); !strings.HasPrefix(body, "Applied error-spike to this pod") {
		t.Errorf("control text: %q", body)
	}
	rr = do(s, "PUT", "/loadgen", "enabled=1&rps=50", "User-Agent", "curl/8.7.1")
	if body := rr.Body.String(); !strings.HasPrefix(body, "Sending 50 req/s") {
		t.Errorf("loadgen text: %q", body)
	}
	rr = do(s, "GET", "/control", "", "User-Agent", "curl/8.7.1")
	if body := rr.Body.String(); !strings.HasPrefix(body, "this pod: error-spike (50% 500 errors)") {
		t.Errorf("control get text: %q", body)
	}
	rr = do(s, "POST", "/stats/reset", "", "User-Agent", "curl/8.7.1")
	if rr.Body.String() != "window reset\n" {
		t.Errorf("reset text: %q", rr.Body.String())
	}
}

func TestEventsTextStream(t *testing.T) {
	s := newTestServer(t)
	srv := httptest.NewServer(s.mux)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/events?cols=4&color=none", nil)
	req.Header.Set("User-Agent", "curl/8.7.1")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type %s", ct)
	}

	var mu sync.Mutex
	var got strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			mu.Lock()
			got.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	// ok, fail, fail, slow — one row at cols=4. The stats are recorded once
	// (so the summary is stable at 50/50 with v2 all errors) and the hits are
	// re-published until the subscriber has seen a whole row.
	hits := []Hit{{Version: "v1", Status: 200, LatencyMs: 1}, {Version: "v2", Status: 500}, {Version: "v2", Err: "timeout"}, {Version: "v1", Status: 200, LatencyMs: 900}}
	for _, h := range hits {
		s.stats.Record(h)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, h := range hits {
			s.broker.PublishHit(h)
		}
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		out := got.String()
		mu.Unlock()
		if strings.Contains(out, "v2 50%") || time.Now().After(deadline) {
			break
		}
	}
	mu.Lock()
	out := got.String()
	mu.Unlock()
	if !strings.Contains(out, "traffic is off") {
		t.Errorf("missing hint when traffic is off:\n%s", out)
	}
	if !strings.Contains(out, "1xx1") {
		t.Errorf("row glyphs: want ok, fail, fail, slow as 1xx1 in\n%s", out)
	}
	if !strings.Contains(out, "v1 50%") || !strings.Contains(out, "v2 50% ✕100%") {
		t.Errorf("row summary:\n%s", out)
	}
}
