# syntax=docker/dockerfile:1
# Base images are pinned by digest (Dependabot keeps the digests current).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.build=${VERSION}" -o /out/canarybird ./cmd/canarybird

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
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
