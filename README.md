# 🐤 canarybird

[![ci](https://github.com/thiha-min-thant/canarybird/actions/workflows/ci.yml/badge.svg)](https://github.com/thiha-min-thant/canarybird/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/thiha-min-thant/canarybird/badge)](https://scorecard.dev/viewer/?uri=github.com/thiha-min-thant/canarybird)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**A canary-deployment test harness you can actually watch.**
One image. Behaviour set at runtime, not baked into tags. A live grid that shows the *real* traffic split, a panel to break only the version you choose, and Prometheus metrics your rollout controller can gate on.

> `docs/demo.gif` — v2 goes green, you hit **error-spike**, Argo rolls it back.
> *(record this with `make kind-demo` + any screen recorder; 20 seconds is plenty)*

```bash
kubectl apply -f https://raw.githubusercontent.com/thiha-min-thant/canarybird/main/examples/quickstart.yaml
kubectl port-forward svc/canarybird 8080:80
open http://localhost:8080
```

That's 3 × `v1` and 1 × `v2` behind one Service. The grid fills up at 30 req/s. Type `v1=75,v2=25` into *Expected split* and see how far reality drifts from the endpoint math. Then choose **Apply to: v2 only → error-spike** and watch the green squares get slashed.

## Why another demo app?

| | rollouts-demo | podinfo | emojivoto | **canarybird** |
|---|---|---|---|---|
| Live visual of the split | ✅ | ❌ | ❌ | ✅ |
| Break a version **at runtime** (no new image) | ❌ tags only | ✅ per request | ❌ | ✅ per version, every pod |
| Prometheus metrics for analysis gates | ❌ | ✅ | via mesh | ✅ server + client side |
| Observed vs. expected split | ❌ | ❌ | ❌ | ✅ |
| Keep-alive pinning demo | ❌ | ❌ | ❌ | ✅ |
| Readiness / crash / OOM scenarios | ❌ | partly | ❌ | ✅ |
| Single container, no mesh needed | ✅ | ✅ | ❌ | ✅ (stdlib only) |

The thing none of them show is the gap between what your controller *claims* (`setWeight: 10`) and what clients *observe*. Sticky sessions, keep-alive pinning, uneven endpoint slices and ingress weight bugs all live in that gap. canarybird measures it from inside the pod, which is the only vantage point that sees the truth.

## What you get

**The grid.** Every square is one request from the built-in load generator to the Service. Colour = version that answered. Red slash = 5xx or connection error. Faded = slower than 500 ms. The yellow outline is the newest request. Legend and a per-version table (share, expected, Δ, error %, p50, p95, pods seen) update once a second.

**The Traffic panel.** Start/stop the load generator, set the rate (5–500 req/s), point it at any URL (the Service, an Ingress, a Gateway), and toggle keep-alive to demonstrate how a chatty client turns a 90/10 split into 100/0.

**The Break-something panel.** Pick a target version, hit a scenario or drag the sliders, apply. With `CONTROL_PEERS` set, the request fans out to every pod through the headless Service and each pod applies it only if its `VERSION` matches.

| Scenario | What it does |
|---|---|
| `healthy` | reset |
| `flaky` | 10% 500s, up to 300 ms jitter |
| `slow-burn` | +800–1200 ms on every request |
| `error-spike` | 50% 500s |
| `cpu-burn` | 40 ms busy-loop per request |
| `memory-leak` | holds 256 MiB resident (drag past your limit to see an OOMKill) |
| `not-ready` | `/readyz` → 503, pod leaves the endpoint slice |
| `crash-loop` | process panics every 200 requests → CrashLoopBackOff |

Sliders: error rate, latency, jitter, CPU burn, memory held, readiness. Custom values are `scenario: custom`.

**Metrics.** `/metrics`, no dependencies:

```promql
# success rate of the version under test
sum(rate(canarybird_http_requests_total{version="v2",status!~"5.."}[1m]))
  / sum(rate(canarybird_http_requests_total{version="v2"}[1m]))

# p95 of the version under test
histogram_quantile(0.95, sum(rate(canarybird_http_request_duration_seconds_bucket{version="v2"}[1m])) by (le))

# what the load generator observed, by who answered
sum by (target_version) (rate(canarybird_loadgen_requests_total[1m]))
```

Also exported: `canarybird_chaos_*` gauges (what is currently configured), `canarybird_ready`, `canarybird_build_info`, `canarybird_sse_clients`.

## No browser? curl it

Half of real clusters are private: you are on a jump host with `kubectl` and
`curl`, and nothing opens `http://localhost:8080`. canarybird notices a curl
(or wget) user agent and answers in text, in colour, so the terminal gets the
same picture as the browser:

```bash
kubectl port-forward svc/canarybird 8080:80 &

curl localhost:8080             # the dashboard: traffic, chaos, per-version split with bars
curl -N localhost:8080/events   # the grid, live: one glyph per request, a split summary per row
```

```
canarybird dev  pod canarybird-v1-7d9f4-x2kq8 · version v1 · up 5s
traffic: 40 req/s → http://canarybird  expected: v1=75,v2=25
this pod: healthy

VERSION  SHARE                          OBSERVED EXPECTED  DELTA  ERRORS     P50     P95  PODS
v1       ███████████████████████│░░░░░░    75.2%    75.0%   +0.2    0.0%    1 ms    2 ms     3
v2       ███████░│░░░░░░░░░░░░░░░░░░░░░    24.8%    25.0%   -0.2   28.8%    2 ms    2 ms     1
```

```
█✕███████████✕█████████✕███✕███✕███████✕██  v1 75%/75 · v2 25%/25 ✕34%
█✕███████████████✕████████✕███✕█████████✕█  v1 75%/75 · v2 25%/25 ✕36%
```

In the grid, colour is the version that answered, `✕` is a 5xx or connection
error and `▒` is slower than 500 ms; the yellow `│` in a bar is the expected
share. `?cols=60` sets the row width, `?color=none` drops the ANSI codes (versions
become their last letter: `1`, `2`; errors `x`). The control routes answer in
one line too, so a whole rollout rehearsal fits in a shell:

```bash
curl -X PUT -d 'enabled=1&rps=50&target=http://canarybird&expected=v1=90,v2=10' localhost:8080/loadgen
curl -X PUT -d 'scenario=error-spike&target_version=v2' localhost:8080/control
curl -X POST localhost:8080/stats/reset
```

Programs are unaffected: `Accept: application/json` (or `?format=json`) keeps the
JSON, browsers and htmx keep HTML and SSE, and `?format=text` or `Accept: text/plain`
forces text from any client.

## Run it with your rollout controller

Each example is a complete manifest with a comment block on how to trigger a rollout and make it fail:

- [`examples/argo-rollouts/`](examples/argo-rollouts/) — Rollout with canary steps + `AnalysisTemplate` on success rate and p95
- [`examples/flagger/`](examples/flagger/) — `Canary` + `MetricTemplate`
- [`examples/gateway-api/`](examples/gateway-api/) — weighted `HTTPRoute`
- [`examples/nginx-ingress/`](examples/nginx-ingress/) — canary annotations
- [`charts/canarybird`](charts/canarybird/) — Helm chart; `helm upgrade --set version=v2 --set color=green` is a rollout

The trick that makes it all work: **a new version is just `VERSION=v2`**. Same image, same tag. You never wait on a build to test a rollout path.

```bash
helm install canarybird charts/canarybird
helm upgrade canarybird charts/canarybird --reuse-values --set version=v2 --set color=green
```

## Configuration

| Env | Default | |
|---|---|---|
| `PORT` | `8080` | |
| `VERSION` | `v1` | what this pod answers as — the one thing you change per rollout |
| `COLOR` | derived from `VERSION` | `blue green yellow purple orange pink teal white`, or any CSS colour |
| `POD_NAME`, `NODE_NAME`, `NAMESPACE`, `ZONE` | hostname / empty | set via the downward API |
| `LOADGEN_ENABLED` | `false` | start generating traffic on boot |
| `LOADGEN_RPS` | `20` | 1–500 |
| `LOADGEN_KEEPALIVE` | `false` | `true` pins connections to a pod |
| `LOADGEN_TARGET` | `http://localhost:$PORT` | set to the Service so traffic crosses the real split |
| `CONTROL_PEERS` | unset | headless Service name; enables fan-out of `/control` |
| `SHUTDOWN_DELAY` | `2s` | time between failing `/readyz` and closing on SIGTERM |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `json` | `debug` logs every hit; `text` for humans |

## HTTP API

| Route | |
|---|---|
| `GET /` | the UI; text dashboard for curl/wget or `?format=text` |
| `GET /api/hit` | the endpoint traffic is sent to; honours the chaos profile; returns identity JSON + `X-Canarybird-Version/Color/Pod` headers |
| `GET /api/version` | identity, no chaos |
| `GET /api/state` | everything: identity, chaos, loadgen, observed split; text table for curl/wget or `?format=text` |
| `PUT /control` | form or `Accept: application/json`; fields `scenario`, `target_version`, `error_pct`, `latency_ms`, `jitter_ms`, `cpu_burn_ms`, `mem_hold_mb`, `ready`, `panic_every` |
| `PUT /loadgen` | `enabled`, `rps`, `keepalive`, `target`, `expected` (e.g. `v1=90,v2=10`) |
| `GET /events` | SSE: `hello`, `hit` (JSON per request), `stats` and `control` (HTML fragments for htmx); for curl `-N` a live text grid, `?cols=`, `?color=none` |
| `GET /metrics`, `/healthz`, `/readyz` | |

```bash
# break every v2 pod from the CLI (add -H 'Accept: application/json' for a machine-readable answer)
curl -X PUT -d 'scenario=error-spike&target_version=v2' http://localhost:8080/control
```

## How it's built

- Go, standard library only. Prometheus exposition is ~150 lines in `internal/telemetry`.
- UI is htmx for the panels and forms (hypermedia does that well) and ~120 lines of vanilla JS on a `<canvas>` for the grid (hypermedia does not do 500 cells/sec well). Both share one `/events` stream. Vendored, no CDN, works air-gapped.
- Distroless, non-root, read-only filesystem, multi-arch (`amd64`, `arm64`). Image is a few megabytes.
- Graceful shutdown: SIGTERM fails `/readyz` first, waits `SHUTDOWN_DELAY`, then closes — so you can also demo zero-downtime (or prove you don't have it).

```bash
make run          # local, traffic hits itself
make test
make fuzz         # Go native fuzz targets for every input parser
make kind-demo    # 3×v1 + 1×v2 on kind with a locally built image
```

Standalone binaries for Linux, macOS and Windows are attached to each
[GitHub release](https://github.com/thiha-min-thant/canarybird/releases), with a
Sigstore-signed `checksums.txt` ([how to verify](SECURITY.md#verifying-a-release)).

## Security

canarybird is a test harness with **no authentication**: anyone who can reach
port 8080 can break the pods. Keep it off the internet, reach the UI with
`kubectl port-forward` or behind an authenticating ingress, and ideally fence
it with a `NetworkPolicy`. Images are distroless, non-root, read-only, pinned by
digest, and shipped with SLSA provenance and an SBOM; release binaries are
signed through Sigstore. Report vulnerabilities privately via
[SECURITY.md](SECURITY.md).

## Roadmap

- v0.2: gRPC endpoint (meshes split it differently), OpenTelemetry traces with exemplars, per-zone colouring
- v0.3: a "replay" mode that records a rollout and exports the observed-vs-expected timeline as a chart
- Ideas and PRs welcome — especially new scenarios that reproduced a real incident for you.

## License

MIT © Thiha Min Thant
