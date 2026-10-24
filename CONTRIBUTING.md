# Contributing

Thanks for taking an interest. Small, focused PRs are easiest to review.

## Before you open a PR

```bash
make lint   # gofmt
make test   # go vet + go test
make helm-lint
```

CI additionally runs `go test -race`, `make fuzz` (every `Fuzz*` target for
30 s), `govulncheck`, CodeQL and a Docker build. Input parsers get a Go native
fuzz test next to their unit tests; `make fuzz FUZZTIME=1m` runs them longer.

`main` only takes changes through a pull request with green checks (a
repository ruleset enforces this); push a branch and open a PR.
Keep the standard-library-only rule: new Go dependencies need a very good
reason, stated in the PR.

## Conventions

- Go: `gofmt`, idiomatic error handling, no global state outside `main`.
- Helm: every new value gets a comment in `values.yaml`; `helm lint --strict`
  must pass.
- Examples under `examples/` must apply cleanly to a fresh cluster and keep
  the hardened `securityContext` block.
- Commits: imperative mood, present tense ("add seccomp profile", not "added").

## Security issues

Do not open a public issue. See [SECURITY.md](SECURITY.md).

## Licence

By contributing you agree your work is released under the MIT licence in
[LICENSE](LICENSE).
