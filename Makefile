.PHONY: build cli server test release deploy

build: cli server

cli:
	go build -o bin/agentdeck ./cmd/agentdeck

server:
	go build -o bin/agentdeckd ./cmd/agentdeckd

test:
	go vet ./... && go test ./...

# cross-compile the CLI for every machine I own
release:
	@mkdir -p dist
	GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/agentdeck-darwin-arm64 ./cmd/agentdeck
	GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/agentdeck-darwin-amd64 ./cmd/agentdeck
	GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/agentdeck-linux-amd64 ./cmd/agentdeck
	GOOS=linux  GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/agentdeck-linux-arm64 ./cmd/agentdeck
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/agentdeck-windows-amd64.exe ./cmd/agentdeck

# ship the committed tree to the server and rebuild the container there
deploy:
	ssh server 'mkdir -p /opt/agentdeck/src /opt/agentdeck/data && find /opt/agentdeck/src -mindepth 1 -delete'
	git archive --format=tar HEAD | ssh server 'tar -x -C /opt/agentdeck/src'
	ssh server 'cd /opt/agentdeck/src/deploy && docker compose up -d --build && docker image prune -f >/dev/null && docker ps --filter name=agentdeck --format "{{.Names}} {{.Status}} {{.Ports}}"'
