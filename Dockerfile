# Multi-stage Dockerfile for the VSA rate-limiter demo (production-style)
# Build stage
FROM golang:1.24 AS build

# Enable Go modules and turn on CGO-less static-ish build for small image
ENV CGO_ENABLED=0 GO111MODULE=on

WORKDIR /src

# Pre-cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source
COPY . .

# Build the api server
RUN go build -o /out/ratelimiter-api ./cmd/ratelimiter-api

# Runtime stage
FROM gcr.io/distroless/base-debian11 AS runtime

# Listening port for the HTTP API
EXPOSE 8080

# Copy binary
COPY --from=build /out/ratelimiter-api /usr/local/bin/ratelimiter-api

# Default command uses mock adapter; override in docker-compose to use redis
ENTRYPOINT ["/usr/local/bin/ratelimiter-api"]
CMD ["--http_addr=0.0.0.0:8080", "--persistence_adapter=mock"]
