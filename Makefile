COMMIT := $(shell if [ -d .git ]; then git rev-parse HEAD; else echo "unknown"; fi)
SHORTCOMMIT := $(shell echo $(COMMIT) | head -c 7)

all: build

## build: Build client and server binaries
build: client server

## clean: Remove built binaries
clean:
	rm -f ./contrib/client/client*
	rm -f ./contrib/server/server*

## test: Run all tests
test:
	go test -race -coverprofile=/dev/null -covermode=atomic -v ./...

## fuzz: Run fuzz tests
fuzz:
	go test -fuzz=Fuzz -run=^Fuzz ./packet -fuzztime 30s

## vet: Analyze code for potential errors
vet:
	go vet ./...

## fmt: Format code
fmt:
	go fmt ./...

## update: Update dependencies
update:
	go get -u -t
	@-$(MAKE) tidy
	@-$(MAKE) vendor

## tidy: Tidy up go.mod
tidy:
	go mod tidy

## vendor: Update vendored packages
vendor:
	go mod vendor

## lint: Static analysis with staticcheck
lint:
	staticcheck ./...

## client: Build import binary
client:
	cd contrib/client && CGO_ENABLED=0 go build -o client -ldflags="-s -w" -a

## client: Build client with debug symbols
client-debug:
	cd contrib/client && CGO_ENABLED=0 go build -o client-debug -gcflags="all=-N -l" -a

## server: Build import binary
server:
	cd contrib/server && CGO_ENABLED=0 go build -o server -ldflags="-s -w" -a

## server: Build server with debug symbols
server-debug:
	cd contrib/server && CGO_ENABLED=0 go build -o server-debug -gcflags="all=-N -l" -a

profile-server-trace:
	@echo "Profiling server with block profile..."
	@PROM_LISTEN=":9000" ./contrib/server/server-debug -profile trace -peerIdleTimeout 30s -peerLatency 3s -receiverLatency 3s -addr 172.16.40.46:6001 -logtopics "config"

profile-server-block:
	@echo "Profiling server with block profile..."
	@PROM_LISTEN=":9000" ./contrib/server/server-debug -profile block -peerIdleTimeout 30s -peerLatency 3s -receiverLatency 3s -addr 172.16.40.46:6001 -logtopics "config"

profile-server-block-http:
	go tool pprof -http=0.0.0.0:6060 ./contrib/server/server-debug block.pprof

## run-server: Start the SRT server (default: :6001)
run-server:
	@echo "Starting SRT server on :6001..."
	@./contrib/server/server -addr :6001

## metrics: Query Prometheus metrics endpoint (default: http://localhost:9000/metrics)
metrics:
	@echo "Querying Prometheus metrics endpoint..."
	@curl -s http://localhost:9000/metrics

## coverage: Generate code coverage analysis
coverage:
	go test -race -coverprofile=cover.out -timeout 60s -v ./...
	go tool cover -html=cover.out -o cover.html

## commit: Prepare code for commit (vet, fmt, test)
commit: vet fmt lint test
	@echo "No errors found. Ready for a commit."

## docker: Build standard Docker image
docker:
	docker build -t gosrt:$(SHORTCOMMIT) .

## logtopics: Extract all logging topics
logtopics:
	grep -ERho 'log\("([^"]+)' *.go | sed -E -e 's/log\("//' | sort -u

.PHONY: all build clean help test fuzz vet fmt vendor commit coverage lint \
	client client-debug server server-debug update tidy logtopics \
	run-server metrics docker

## help: Show all commands
help: Makefile
	@echo
	@echo " Choose a command:"
	@echo
	@sed -n 's/^##//p' $< | column -t -s ':' |  sed -e 's/^/ /'
	@echo
