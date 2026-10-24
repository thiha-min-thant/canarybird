# Security policy

## Supported versions

Only the latest release (`ghcr.io/thiha-min-thant/canarybird:latest` and the
most recent `vX.Y.Z` tag) receives security fixes.

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Use GitHub's private vulnerability reporting:
<https://github.com/thiha-min-thant/canarybird/security/advisories/new>

You should get an acknowledgement within 7 days. Once a fix is released the
advisory is published with credit to the reporter (unless you prefer otherwise).

## Threat model — read this before deploying

canarybird is a **test harness**, not a production service. By design it has
no authentication: anyone who can reach port 8080 can

- `PUT /control` — make this pod (or every pod of a chosen version) return
  errors, add latency, burn CPU, hold memory, fail readiness or crash;
- `PUT /loadgen` — start or stop the built-in load generator.

That is the point of the tool, but it means:

- **Never expose canarybird to the internet** or to untrusted networks. Reach
  the UI with `kubectl port-forward`, or put it behind an authenticating
  ingress / mesh policy.
- Prefer a dedicated namespace with a `NetworkPolicy` that only admits traffic
  from the rollout controller, Prometheus and your own tooling.
- Treat `CONTROL_PEERS` as trusted: the pod will send `/control` requests to
  every address that name resolves to.

What the project does to keep the blast radius small:

- Static Go binary, standard library only (no third-party Go dependencies).
- Distroless, non-root (uid 65532), read-only root filesystem, all
  capabilities dropped, `RuntimeDefault` seccomp, no service-account token.
- Base images pinned by digest; GitHub Actions pinned by commit SHA; CI runs
  `govulncheck`, CodeQL and OpenSSF Scorecard; Dependabot keeps pins current.
- Release images carry SLSA provenance and an SBOM attestation.
- All runtime inputs are clamped (`internal/chaos/chaos.go`): memory hold is
  capped at 4 GiB, CPU burn at 5 s, latency and jitter at 30 s.
