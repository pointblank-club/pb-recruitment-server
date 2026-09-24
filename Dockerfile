FROM golang:1.25.1-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

COPY go.mod go.sum ./

RUN go mod download && go mod verify

COPY . .

# Build the application
# CGO_ENABLED=0 for static binary
# -ldflags="-s -w" to strip debug info and reduce binary size
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o /build/app \
    ./cmd/app/main.go

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

# Create non-root user for security
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser

WORKDIR /app

COPY --from=builder /build/app /app/app

COPY --from=builder /build/internal/migrations /app/internal/migrations

# Create empty .env file to prevent loading errors when STAGE != production
RUN touch /app/.env

# Change ownership to non-root user
RUN chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

CMD ["/app/app"]

