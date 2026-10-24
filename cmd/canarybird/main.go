// canarybird is a single-binary canary-deployment test harness: one image,
// behaviour controlled at runtime, with a live grid that shows the real
// traffic split and a chaos panel to break only the version you choose.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/thiha-min-thant/canarybird/internal/server"
)

// Set by -ldflags "-X main.build=v0.1.0".
var build = "dev"

func main() {
	log := newLogger()
	port := envOr("PORT", "8080")

	cfg := server.Config{
		Port:         port,
		Build:        build,
		ControlPeers: os.Getenv("CONTROL_PEERS"),
		Loadgen: server.LoadgenSettings{
			Enabled:   envBool("LOADGEN_ENABLED", false),
			RPS:       envInt("LOADGEN_RPS", 20),
			KeepAlive: envBool("LOADGEN_KEEPALIVE", false),
			Target:    envOr("LOADGEN_TARGET", "http://localhost:"+port),
		},
		Log: log,
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil && err.Error() != "http: Server closed" {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
	log.Info("bye")
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	var h slog.Handler
	if strings.ToLower(os.Getenv("LOG_FORMAT")) == "text" {
		h = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	return slog.New(h).With("app", "canarybird", "version", os.Getenv("VERSION"))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
