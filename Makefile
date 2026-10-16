.PHONY: build linux-amd64 linux-arm64 test

build:
	go build -o dist/fbm ./cmd/fbm

linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/fbm-linux-amd64 ./cmd/fbm

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/fbm-linux-arm64 ./cmd/fbm

test:
	go test ./...
