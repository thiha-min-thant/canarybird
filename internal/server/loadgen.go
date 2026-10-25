package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LoadgenSettings is everything the Traffic panel controls.
type LoadgenSettings struct {
	Enabled   bool   `json:"enabled"`
	RPS       int    `json:"rps"`
	KeepAlive bool   `json:"keepalive"` // on = connections pin to one pod; off = every request re-balances
	Target    string `json:"target"`    // base URL, usually the Service, so traffic crosses the real split
}

// Loadgen generates traffic from inside the pod against Target and records
// what it sees. Because it is the *client*, its view of the split is the truth
// the rollout controller has to be measured against.
type Loadgen struct {
	mu        sync.RWMutex
	s         LoadgenSettings
	stats     *Stats
	broker    *Broker
	metrics   *Metrics
	log       *slog.Logger
	keepAlive *http.Client
	oneShot   *http.Client
	wake      chan struct{}
	inflight  sync.WaitGroup
}

func NewLoadgen(s LoadgenSettings, stats *Stats, broker *Broker, m *Metrics, log *slog.Logger) *Loadgen {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	mk := func(disableKeepAlive bool) *http.Client {
		return &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext:         dialer.DialContext,
				DisableKeepAlives:   disableKeepAlive,
				MaxIdleConnsPerHost: 64,
				IdleConnTimeout:     30 * time.Second,
			},
		}
	}
	return &Loadgen{
		s: clampLoadgen(s), stats: stats, broker: broker, metrics: m, log: log,
		keepAlive: mk(false), oneShot: mk(true), wake: make(chan struct{}, 1),
	}
}

func clampLoadgen(s LoadgenSettings) LoadgenSettings {
	s.RPS = min(max(s.RPS, 1), 500)
	s.Target = strings.TrimRight(strings.TrimSpace(s.Target), "/")
	if !strings.HasPrefix(s.Target, "http://") && !strings.HasPrefix(s.Target, "https://") {
		s.Target = "http://" + s.Target
	}
	return s
}

func (l *Loadgen) Get() LoadgenSettings {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.s
}

func (l *Loadgen) Set(s LoadgenSettings) LoadgenSettings {
	s = clampLoadgen(s)
	l.mu.Lock()
	changedTarget := l.s.Target != s.Target
	l.s = s
	l.mu.Unlock()
	if changedTarget {
		l.stats.Reset()
	}
	l.keepAlive.Transport.(*http.Transport).CloseIdleConnections()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	l.log.Info("loadgen updated", "enabled", s.Enabled, "rps", s.RPS, "keepalive", s.KeepAlive, "target", s.Target)
	return s
}

// Run drives the ticker until ctx is cancelled.
func (l *Loadgen) Run(ctx context.Context) {
	for {
		s := l.Get()
		if !s.Enabled {
			select {
			case <-ctx.Done():
				l.inflight.Wait()
				return
			case <-l.wake:
				continue
			}
		}
		interval := time.Second / time.Duration(s.RPS)
		t := time.NewTicker(interval)
	loop:
		for {
			select {
			case <-ctx.Done():
				t.Stop()
				l.inflight.Wait()
				return
			case <-l.wake:
				t.Stop()
				break loop
			case <-t.C:
				l.inflight.Add(1)
				go func() {
					defer l.inflight.Done()
					l.one(ctx)
				}()
			}
		}
	}
}

func (l *Loadgen) one(ctx context.Context) {
	s := l.Get()
	client := l.oneShot
	if s.KeepAlive {
		client = l.keepAlive
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Target+"/api/hit", nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Canarybird-Loadgen", "1")
	start := time.Now()
	resp, err := client.Do(req)
	lat := float64(time.Since(start).Microseconds()) / 1000
	hit := Hit{TS: nowMs(), LatencyMs: lat}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		hit.Err = shortErr(err)
		hit.Status = 0
	} else {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		hit.Status = resp.StatusCode
		hit.Version = resp.Header.Get("X-Canarybird-Version")
		hit.Color = resp.Header.Get("X-Canarybird-Color")
		hit.Pod = resp.Header.Get("X-Canarybird-Pod")
		if hit.Version == "" {
			hit.Version = "unknown"
			hit.Color = "#8A96A8"
		}
	}
	l.stats.Record(hit)
	l.metrics.ObserveClient(hit)
	l.broker.PublishHit(hit)
	if b, err := json.Marshal(hit); err == nil {
		l.broker.Publish("hit", b)
	}
}

func shortErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "no such host"):
		return "dns"
	case strings.Contains(msg, "EOF"):
		return "connection reset"
	}
	if len(msg) > 60 {
		msg = msg[:60]
	}
	return msg
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
