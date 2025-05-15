# db-query-operator/Dockerfile

# Build Stage
FROM golang:1.24 as builder

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
# Use a distroless image for a minimal attack surface
FROM gcr.io/distroless/static-debian11:nonroot AS runtime
# For debugging, you might temporarily use a different base like alpine:
# FROM alpine:latest
# RUN apk add --no-cache ca-certificates

WORKDIR /
# Copy the compiled binary from the builder stage
COPY --from=builder /workspace/manager .
# Copy CA certificates if needed (distroless/static usually includes them)
# COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

USER nonroot:nonroot

# The binary is the entrypoint
ENTRYPOINT ["/manager"]