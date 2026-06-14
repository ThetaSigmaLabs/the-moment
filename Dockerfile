# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies for CGO compilation
RUN apk add --no-cache git build-base

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies with cache mount
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy templates directory separately for better layer caching
COPY templates/ ./templates/

# Copy static files directory
COPY static/ ./static/

# Copy source code
COPY *.go ./

# Build the application with cache mounts
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o main .

# Final stage
FROM alpine:3.24 AS production

# Install runtime dependencies
# Using --no-scripts to work around Alpine trigger script issues with QEMU emulation on arm64
RUN apk add --no-cache --no-scripts ca-certificates sqlite ffmpeg

# Create app directory
WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/main .

# Copy static files from builder stage
COPY --from=builder /app/static ./static

# Create directory for database
RUN mkdir -p /app/data

# Expose port
EXPOSE ${THE_MOMENT_PORT:-5000}

# Set environment variables
ENV GIN_MODE=release
ENV THE_MOMENT_DB_PATH=/app/data

# Run the application
CMD ["./main"]

# ─── CI stage: production image from pre-built binary ───────────────────────
# Used by Jenkins arm64 builds to skip recompiling on slow ARM hardware.
# Caller must place the binary at ./main in the Docker build context.
FROM alpine:3.24 AS production-prebuilt
RUN apk add --no-cache --no-scripts ca-certificates sqlite ffmpeg
WORKDIR /app
COPY main .
COPY static/ ./static/
RUN mkdir -p /app/data
EXPOSE ${THE_MOMENT_PORT:-5000}
ENV GIN_MODE=release
ENV THE_MOMENT_DB_PATH=/app/data
CMD ["./main"]

# ─── Development stage (air hot-reload) ────────────────────────────────────
# Not used in production. Activated via docker-compose.dev.yml build target.
FROM golang:1.24-alpine AS dev

RUN apk add --no-cache git build-base

WORKDIR /app/src

# Pre-cache modules so first air build is fast
COPY go.mod go.sum ./
RUN go mod download

# Install air
RUN go install github.com/air-verse/air@v1.62.0

# Bake air config into the image at a path outside the source mount
RUN printf '[build]\ncmd = "CGO_ENABLED=1 go build -o /tmp/moment-bin ."\nbin = "/tmp/moment-bin"\ninclude_ext = ["go", "html", "js", "css", "tmpl"]\nexclude_dir = ["vendor", "testdata", "dev", "backups"]\ndelay = 500\n\n[misc]\nclean_on_exit = true\n' > /app/air.toml

CMD ["air", "-c", "/app/air.toml"]
