.PHONY: build cli server test release deploy

build: cli server

cli:
	go build -o bin/skillhub ./cmd/skillhub

server:
	go build -o bin/skillhubd ./cmd/skillhubd

test:
	go vet ./... && go test ./...

# cross-compile the CLI for every machine I own
release:
	@mkdir -p dist
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/skillhub-darwin-arm64 ./cmd/skillhub
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/skillhub-darwin-amd64 ./cmd/skillhub
	GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/skillhub-linux-amd64 ./cmd/skillhub
	GOOS=linux  GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/skillhub-linux-arm64 ./cmd/skillhub
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/skillhub-windows-amd64.exe ./cmd/skillhub

# ship the committed tree to the server and rebuild the container there
deploy:
	ssh server 'mkdir -p /opt/skillhub/src /opt/skillhub/data && find /opt/skillhub/src -mindepth 1 -delete'
	git archive --format=tar HEAD | ssh server 'tar -x -C /opt/skillhub/src'
	ssh server 'cd /opt/skillhub/src/deploy && docker compose up -d --build && docker image prune -f >/dev/null && docker ps --filter name=skillhub --format "{{.Names}} {{.Status}} {{.Ports}}"'
