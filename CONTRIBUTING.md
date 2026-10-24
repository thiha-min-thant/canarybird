# Contributing

Thanks for taking an interest. Small, focused PRs are easiest to review.

## Before you open a PR

```bash
make lint   # gofmt
make test   # go vet + go test
make helm-lint
```

CI additionally runs `go test -race`, `govulncheck`, CodeQL and a Docker build.
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
