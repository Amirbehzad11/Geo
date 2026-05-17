BINARY  := geo-service
CMD     := ./cmd/server

.PHONY: run build test lint docker swag tidy loadtest

run:
	go run $(CMD)

build:
	CGO_ENABLED=0 go build -ldflags="-w -s" -trimpath -o $(BINARY) $(CMD)

test:
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

docker:
	docker build -t $(BINARY):latest .

swag:
	swag init -g cmd/server/main.go -o docs --parseDependency --parseInternal

tidy:
	go mod tidy

loadtest:
	go run ./cmd/loadtest -scenario=mixed -c=50 -duration=30s
