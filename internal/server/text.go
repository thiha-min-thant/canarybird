package server

import (
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thiha-min-thant/canarybird/internal/chaos"
)

// The terminal face of canarybird. Half of real clusters are private: you are
// on a jump host with curl and no browser. These renderers make
//
//	curl canarybird            the dashboard: identity, traffic, chaos, split
//	curl -N canarybird/events  the grid, one glyph per request, live
//
// read like the web UI does. Colour is ANSI 24-bit, off with ?color=none.

func textHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// palette is how the renderer colours text: fg(hex) → SGR, or "" when off.
type palette struct{ on bool }

func paletteFor(r *http.Request) palette {
	switch strings.ToLower(r.URL.Query().Get("color")) {
	case "none", "no", "off", "0", "false", "mono":
		return palette{}
	}
	return palette{on: true}
}

func (p palette) fg(hex string) string {
	if !p.on {
		return ""
	}
	r, g, b := rgb(hex)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

func (p palette) bg(hex string) string {
	if !p.on {
		return ""
	}
	r, g, b := rgb(hex)
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

func (p palette) wrap(s string, sgr ...string) string {
	if !p.on {
		return s
	}
	return strings.Join(sgr, "") + s + "\x1b[0m"
}

const (
	sgrBold = "\x1b[1m"
	sgrDim  = "\x1b[2m"
	hexFail = "#FF5C5C"
	hexNew  = "#F5C842"
	hexMute = "#8E9AAD"
	hexGood = "#3DD68C"
)

func (p palette) muted(s string) string { return p.wrap(s, p.fg(hexMute)) }
func (p palette) warn(s string) string  { return p.wrap(s, p.fg(hexFail)) }
func (p palette) good(s string) string  { return p.wrap(s, p.fg(hexGood)) }
func (p palette) bold(s string) string  { return p.wrap(s, sgrBold) }

// rgb accepts #rgb / #rrggbb; anything else (a CSS colour name) is hashed onto
// the palette so the same string is always the same colour.
func rgb(hex string) (r, g, b uint8) {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) == 6 {
		if n, err := strconv.ParseUint(h, 16, 32); err == nil {
			return uint8(n >> 16), uint8(n >> 8), uint8(n)
		}
	}
	f := fnv.New32a()
	f.Write([]byte(hex))
	return rgb(Palette[int(f.Sum32())%len(Palette)].Hex)
}

// ---- GET / and /api/state ----------------------------------------------------

func (s *Server) writeStateText(w http.ResponseWriter, r *http.Request) {
	textHeaders(w)
	s.identityHeaders(w)
	p := paletteFor(r)
	rows, total, secs := s.stats.Snapshot()
	lg := s.loadgen.Get()

	fmt.Fprintf(w, "%s %s  %s\n", p.bold("canarybird"), p.muted(s.cfg.Build),
		p.muted(fmt.Sprintf("pod %s · version %s · up %s", s.id.Pod, s.id.Version, time.Since(s.started).Truncate(time.Second))))
	fmt.Fprintln(w, trafficLine(lg, s.stats.Expected()))
	fmt.Fprintln(w, chaosLine(s.chaos.Get()))
	fmt.Fprintln(w)

	if total == 0 {
		if lg.Enabled {
			fmt.Fprintln(w, p.muted("waiting for the first responses…"))
		} else {
			fmt.Fprintln(w, p.muted("no requests in window"))
		}
		return
	}
	nameW := 7
	for _, row := range rows {
		nameW = max(nameW, len(row.Version))
	}
	const barW = 30
	fmt.Fprintln(w, p.muted(fmt.Sprintf("%-*s  %-*s %8s %8s %6s %7s %7s %7s %5s",
		nameW, "VERSION", barW, "SHARE", "OBSERVED", "EXPECTED", "DELTA", "ERRORS", "P50", "P95", "PODS")))
	for _, row := range rows {
		exp, delta := p.muted("       -"), p.muted("     -")
		if row.Expected >= 0 {
			exp = fmt.Sprintf("%7.1f%%", row.Expected)
			delta = fmt.Sprintf("%+6.1f", row.Delta)
			if row.Delta > 5 || row.Delta < -5 {
				delta = p.warn(delta)
			}
		}
		errs := fmt.Sprintf("%6.1f%%", row.ErrorRate)
		if row.ErrorRate > 0 {
			errs = p.warn(errs)
		}
		fmt.Fprintf(w, "%s  %s %7.1f%% %s %s %s %4.0f ms %4.0f ms %5d\n",
			p.wrap(fmt.Sprintf("%-*s", nameW, row.Version), p.fg(row.Color), sgrBold),
			bar(p, row, barW), row.Share, exp, delta, errs, row.P50Ms, row.P95Ms, row.Pods)
	}
	fmt.Fprintf(w, "\n%s\n", p.muted(fmt.Sprintf("last %d requests over %.1f s · PUT /loadgen to change traffic, PUT /control to break a version, POST /stats/reset to clear", total, secs)))
}

// bar is the share as a filled bar; the yellow mark is the expected share,
// so the gap between the two is the whole point of the tool.
func bar(p palette, row VersionStat, width int) string {
	filled := int(row.Share/100*float64(width) + 0.5)
	cells := make([]string, width)
	for i := range cells {
		if i < filled {
			cells[i] = p.wrap("█", p.fg(row.Color))
		} else {
			cells[i] = p.muted("░")
		}
	}
	if row.Expected >= 0 {
		i := min(int(row.Expected/100*float64(width)+0.5), width-1)
		cells[i] = p.wrap("│", p.fg(hexNew), sgrBold)
	}
	return strings.Join(cells, "")
}

func trafficLine(lg LoadgenSettings, expected string) string {
	line := "traffic: off"
	if lg.Enabled {
		line = fmt.Sprintf("traffic: %d req/s → %s", lg.RPS, lg.Target)
		if lg.KeepAlive {
			line += " (keep-alive: connections pin to a pod)"
		}
	}
	if expected != "" {
		line += "  expected: " + expected
	}
	return line
}

func chaosLine(st chaos.Settings) string {
	line := "this pod: " + st.Scenario
	var bits []string
	if st.ErrorRate > 0 {
		bits = append(bits, fmt.Sprintf("%.0f%% %d errors", st.ErrorRate*100, st.ErrorStatus))
	}
	if st.LatencyMs > 0 || st.JitterMs > 0 {
		bits = append(bits, fmt.Sprintf("+%d ms latency ±%d", st.LatencyMs, st.JitterMs))
	}
	if st.CPUBurnMs > 0 {
		bits = append(bits, fmt.Sprintf("%d ms cpu burn", st.CPUBurnMs))
	}
	if st.MemHoldMB > 0 {
		bits = append(bits, fmt.Sprintf("holding %d MiB", st.MemHoldMB))
	}
	if !st.Ready {
		bits = append(bits, "NOT READY")
	}
	if st.PanicEvery > 0 {
		bits = append(bits, fmt.Sprintf("panics every %d hits", st.PanicEvery))
	}
	if len(bits) > 0 {
		line += " (" + strings.Join(bits, ", ") + ")"
	}
	return line
}

// ---- GET /events --------------------------------------------------------------

// streamHitsText is the grid for curl -N: one glyph per request as it
// happens, colour = version that answered, ✕ = 5xx or connection error,
// ▒ = slower than 500 ms. Every ?cols= glyphs (default 40) the row closes
// with the running split from the stats window.
func (s *Server) streamHitsText(w http.ResponseWriter, r *http.Request, flusher http.Flusher) {
	textHeaders(w)
	s.identityHeaders(w)
	w.Header().Set("X-Accel-Buffering", "no")
	p := paletteFor(r)
	cols := atoiDefault(r.URL.Query().Get("cols"), 40)
	cols = min(max(cols, 10), 200)

	fmt.Fprintf(w, "%s  %s\n", p.bold("canarybird "+s.id.Pod), p.muted("one glyph per request from this pod's load generator"))
	fmt.Fprintf(w, "%s\n", p.muted("█ ok   "+p.warn("✕")+p.muted(" 5xx or connection error   ")+"▒ slower than 500 ms   colour = version that answered"))
	lg := s.loadgen.Get()
	if lg.Enabled {
		fmt.Fprintln(w, p.muted(trafficLine(lg, s.stats.Expected())))
	} else {
		fmt.Fprintln(w, p.warn("traffic is off.")+p.muted(" start it: curl -X PUT -d 'enabled=1&rps=30' "+requestBase(r)+"/loadgen"))
	}
	fmt.Fprintln(w)
	flusher.Flush()

	ch, cancel := s.broker.SubscribeHits()
	defer cancel()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	n := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case h := <-ch:
			io.WriteString(w, glyph(p, h))
			n++
			if n%cols == 0 {
				fmt.Fprintf(w, "  %s\n", s.rowSummary(p))
			}
			flusher.Flush()
		case <-keepalive.C:
			if n%cols != 0 {
				continue // mid-row: a glyph will flush soon enough
			}
			flusher.Flush()
		}
	}
}

func glyph(p palette, h Hit) string {
	if !p.on {
		switch {
		case h.Err != "" || h.Status >= 500:
			return "x"
		case h.LatencyMs > 500:
			return strings.ToUpper(letter(h.Version))
		}
		return letter(h.Version)
	}
	switch {
	case h.Err != "" || h.Status >= 500:
		return p.wrap("✕", p.fg(hexFail), sgrBold)
	case h.LatencyMs > 500:
		return p.wrap("▒", p.fg(h.Color))
	}
	return p.wrap("█", p.fg(h.Color))
}

// letter is the no-colour glyph: the last character of the version, so v1/v2
// read as 1/2 and blue/green as e/n; good enough to tell them apart.
func letter(version string) string {
	if version == "" {
		return "?"
	}
	return strings.ToLower(version[len(version)-1:])
}

func (s *Server) rowSummary(p palette) string {
	rows, _, _ := s.stats.Snapshot()
	parts := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		part := p.wrap(r.Version, p.fg(r.Color), sgrBold) + fmt.Sprintf(" %.0f%%", r.Share)
		if r.Expected >= 0 {
			part += p.muted(fmt.Sprintf("/%.0f", r.Expected))
		}
		if r.ErrorRate > 0 {
			part += " " + p.warn(fmt.Sprintf("✕%.0f%%", r.ErrorRate))
		}
		parts = append(parts, part)
	}
	if st := s.chaos.Get(); st.Scenario != "healthy" {
		parts = append(parts, p.muted("this pod: ")+p.warn(st.Scenario))
	}
	return strings.Join(parts, p.muted(" · "))
}

func requestBase(r *http.Request) string {
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	return "http://" + host
}
