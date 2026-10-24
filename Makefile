IMAGE   ?= ghcr.io/thiha-min-thant/canarybird
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: run build test lint docker docker-push kind-demo kind-delete helm-lint

run: ## run locally with traffic on, hitting itself
	LOADGEN_ENABLED=true LOG_FORMAT=text go run ./cmd/canarybird

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.build=$(VERSION)" -o bin/canarybird ./cmd/canarybird

test:
	go vet ./... && go test ./...

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
