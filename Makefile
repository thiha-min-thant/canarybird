IMAGE   ?= ghcr.io/thiha-min-thant/canarybird
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: run build test fuzz lint docker docker-push kind-demo kind-delete helm-lint

# A bare `make` must build, not launch the server: CodeQL's Go autobuilder and
# other tooling run `make` with no target and would otherwise hang on `run`.
.DEFAULT_GOAL := build

run: ## run locally with traffic on, hitting itself
	LOADGEN_ENABLED=true LOG_FORMAT=text go run ./cmd/canarybird

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.build=$(VERSION)" -o bin/canarybird ./cmd/canarybird

test:
	go vet ./... && go test ./...

FUZZTIME ?= 10s
fuzz: ## run every Fuzz* target for $(FUZZTIME) each (seed corpora already run under `make test`)
	@set -e; for pkg in $$(go list ./...); do \
	  for fn in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
	    echo "== $$pkg $$fn"; go test $$pkg -run='^$$' -fuzz="^$$fn\$$" -fuzztime=$(FUZZTIME); \
	  done; \
	done

lint:
	gofmt -l . && test -z "$$(gofmt -l .)"

docker:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) --load .

docker-push:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .

helm-lint:
	helm lint charts/canarybird

kind-demo: ## two-version demo on a local kind cluster
	./hack/kind-demo.sh

kind-delete:
	kind delete cluster --name canarybird
