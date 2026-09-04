# ---- build stage ----
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Cache dependencies before copying source
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -trimpath \
    -o /app/geo-service ./cmd/server

# ---- runtime stage ----
FROM alpine:3.20

WORKDIR /app
COPY --from=builder /app/geo-service .
# The binary is statically linked. Copy the builder's CA bundle instead of
# downloading Alpine packages during the image build (important on restricted
# servers where dl-cdn.alpinelinux.org is slow or blocked).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

EXPOSE 8080
ENTRYPOINT ["./geo-service"]
