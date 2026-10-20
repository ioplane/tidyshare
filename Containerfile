FROM docker.io/library/golang:1.27.3-trixie@sha256:1896654cfc5c2b5ad74c07f29cb36f5d2d1cbed42e3054070fbfe444e521e83c AS tools

ARG GOFUMPT_VERSION=v0.12.0
ARG GOLANGCI_LINT_VERSION=v2.15.0
ARG GOVULNCHECK_VERSION=v1.9.0

RUN GOBIN=/opt/tools go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && GOBIN=/opt/tools go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION} \
    && GOBIN=/opt/tools go install mvdan.cc/gofumpt@${GOFUMPT_VERSION}

FROM docker.io/library/golang:1.27.3-trixie@sha256:1896654cfc5c2b5ad74c07f29cb36f5d2d1cbed42e3054070fbfe444e521e83c

LABEL org.opencontainers.image.source="https://github.com/ioplane/tidyshare" \
      org.opencontainers.image.description="tidyshare development and verification toolchain"

ENV CGO_ENABLED=0 \
    GOCACHE=/tmp/go-build \
    GOMODCACHE=/opt/go/pkg/mod \
    GOFLAGS=-buildvcs=false

COPY --from=tools /opt/tools/ /usr/local/bin/

WORKDIR /opt/tidyshare/module
COPY go.mod go.sum ./
RUN go mod download \
    && chmod -R a+rX /opt/go \
    && git config --system --add safe.directory /workspace

WORKDIR /workspace
