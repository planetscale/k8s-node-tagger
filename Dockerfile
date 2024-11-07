# syntax=docker/dockerfile:1.9
FROM pscale.dev/wolfi-prod/go:1.23 AS builder

WORKDIR /work
COPY . /work

ENV CGO_ENABLED=0
RUN go build -trimpath -v -o ./k8s-node-tagger .

# -- runtime image: --
FROM pscale.dev/wolfi-prod/static:latest

COPY --from=builder /work/k8s-node-tagger /k8s-node-tagger

ENTRYPOINT ["/k8s-node-tagger"]
