.PHONY: lint build test cov run

default: build

build:
	CGO_ENABLED=0 go build -trimpath -v .

lint:
	golangci-lint run -v ./...

test:
	go test -race -cover -coverprofile=cover.out -v ./...

cov:
	@echo "--- Coverage:"
	go tool cover -html=cover.out
	go tool cover -func cover.out

run:
	go run .
