# ---- build stage ----
FROM golang:1.23-alpine AS builder

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

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/geo-service .

EXPOSE 8080
ENTRYPOINT ["./geo-service"]
