# syntax=docker/dockerfile:1
# Base images are pinned by digest (Dependabot keeps the digests current).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.build=${VERSION}" -o /out/canarybird ./cmd/canarybird

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
ARG VERSION=dev
LABEL org.opencontainers.image.title="canarybird" \
      org.opencontainers.image.description="Canary-deployment test harness: live traffic grid, runtime chaos controls, Prometheus metrics" \
      org.opencontainers.image.source="https://github.com/thiha-min-thant/canarybird" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/canarybird /canarybird
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/canarybird"]
