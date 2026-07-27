# Build the manager binary
FROM golang:1.26.5 AS builder

ARG BUILDOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build
RUN CGO_ENABLED=0 GOOS=$BUILDOS GOARCH=$TARGETARCH go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot

ARG GIT_VERSION=unknown

LABEL org.opencontainers.image.source="https://github.com/projectsveltos/event-manager" \
      org.opencontainers.image.url="https://projectsveltos.io" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.vendor="projectsveltos" \
      org.opencontainers.image.title="event-manager" \
      org.opencontainers.image.description="Watches for events in managed clusters and generates ClusterProfiles on the fly." \
      org.opencontainers.image.version="$GIT_VERSION" \
      org.opencontainers.image.revision="$GIT_VERSION"

WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
