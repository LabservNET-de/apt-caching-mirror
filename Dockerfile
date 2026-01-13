# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache gcc musl-dev sqlite-dev

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o apt-cache-proxy .

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk --no-cache add ca-certificates sqlite-libs

# Copy binary from builder
COPY --from=builder /build/apt-cache-proxy .

# Copy config template
COPY config.json .

# Create directories
RUN mkdir -p storage data

# Expose port
EXPOSE 8080

# Run the application
CMD ["./apt-cache-proxy"]
