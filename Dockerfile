# syntax=docker/dockerfile:1.15@sha256:05e0ad437efefcf144bfbf9d7f728c17818408e6d01432d9e264ef958bbd52f3
FROM --platform=$BUILDPLATFORM pscale.dev/wolfi-prod/go:1.24 AS builder
WORKDIR /work

RUN \
  --mount=type=cache,target=/go/pkg/mod,sharing=locked \
  --mount=type=bind,source=go.mod,target=go.mod \
  --mount=type=bind,source=go.sum,target=go.sum \
  go mod download

COPY . .
ENV CGO_ENABLED=0
ARG TARGETOS
ARG TARGETARCH

RUN \
  --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
  --mount=type=cache,target=/go/pkg/mod,sharing=locked \
  GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -v -o ./k8s-node-tagger .

FROM pscale.dev/wolfi-prod/static:latest
COPY --from=builder /work/k8s-node-tagger /k8s-node-tagger
ENTRYPOINT ["/k8s-node-tagger"]