# db-query-operator/Dockerfile

# Build Stage
FROM golang:1.27 as builder

WORKDIR /workspace

# Copy Go modules and source code
COPY go.mod go.mod
COPY go.sum go.sum
# Download dependencies first to leverage Docker cache
RUN go mod download

COPY api/ api/
COPY internal/ internal/
COPY main.go main.go

# Build the binary
# CGO_ENABLED=0 prevents linking against C libraries
# GOOS=linux forces Linux binary format
# GOARCH=amd64 specifies the architecture (adjust if needed, e.g., arm64)
# -ldflags="-w -s" strips debug information and symbol table for smaller binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags="-w -s" -o manager main.go

# Runtime Stage
# Use a distroless image for a minimal attack surface.
# Pinned to a specific Alpine series (not floating `latest`, which drifted into
# a vulnerable openssl build) — `alpine:3.24` carries the openssl 3.5.8-r0 fix
# for CVE-2026-18798 / CVE-2026-75803 (Alpine security tracker: fixed).
# `apk upgrade` at build time additionally guarantees the image always ships the
# newest patched packages from the series repo, whatever the base layer contains.
FROM alpine:3.24 AS runtime

RUN apk add --no-cache ca-certificates && apk upgrade --no-cache

WORKDIR /
# Copy the compiled binary from the builder stage
COPY --from=builder /workspace/manager .

# Use a non-root user (nobody:65534)
USER 65534:65534

# The binary is the entrypoint
ENTRYPOINT ["/manager"]