FROM docker.io/library/golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9 AS tools

ARG GOFUMPT_VERSION=v0.12.0
ARG GOLANGCI_LINT_VERSION=v2.15.0
ARG GOVULNCHECK_VERSION=v1.9.0

RUN GOBIN=/opt/tools go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} \
    && GOBIN=/opt/tools go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION} \
    && GOBIN=/opt/tools go install mvdan.cc/gofumpt@${GOFUMPT_VERSION}

FROM docker.io/library/golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9

LABEL org.opencontainers.image.source="https://github.com/ioplane/tidyshare" \
      org.opencontainers.image.description="tidyshare development and verification toolchain"

ENV CGO_ENABLED=0 \
    GOCACHE=/tmp/go-build \
    GOMODCACHE=/opt/go/pkg/mod \
    GOFLAGS="-buildvcs=false -tags=remote,exclude_graphdriver_btrfs,containers_image_openpgp"

COPY --from=tools /opt/tools/ /usr/local/bin/

WORKDIR /opt/tidyshare/module
COPY go.mod go.sum ./
RUN go mod download \
    && chmod -R a+rX /opt/go \
    && git config --system --add safe.directory /workspace

WORKDIR /workspace
