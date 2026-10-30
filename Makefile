.PHONY: test dist

test:
	go vet ./... && go test ./...

dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/uptime-keeper-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/uptime-keeper-linux-arm64 .
