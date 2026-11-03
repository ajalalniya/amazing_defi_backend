.PHONY: run run-live test lint tidy docker docker-live docker-both docker-down

# Simulated markets, demo positions. No RPC required.
run:
	go run ./cmd/amazingd

# Live Uniswap v3 spots via RPC (see BASE_RPC_URL). Port still 8080 unless PORT is set.
run-live:
	PROVIDER=chain go run ./cmd/amazingd

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy

docker:
	docker compose up --build -d

docker-live:
	docker compose --profile live up --build -d api-live

docker-both:
	docker compose --profile live up --build -d

docker-down:
	docker compose --profile live down
