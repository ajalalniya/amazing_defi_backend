.PHONY: run test lint tidy

run:
	PROVIDER=mock go run ./cmd/amazingd

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy
