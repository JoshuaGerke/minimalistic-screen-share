# minimalistic-screen-share – baut die drei Binaries.
# Auf Windows lieber build.ps1 nehmen.

GO ?= go
BIN := bin
LDFLAGS := -s -w

.PHONY: all clean test vet fmt windows linux linux-arm viewer

all: share relay viewer

share:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-share ./cmd/share

relay:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-relay ./cmd/relay

viewer:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-viewer ./cmd/viewer

windows:
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS) -H=windowsgui" -o $(BIN)/mss-share.exe ./cmd/share
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-relay.exe ./cmd/relay
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-viewer.exe ./cmd/viewer

linux: linux-amd64
linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-share-linux-amd64 ./cmd/share
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-relay-linux-amd64 ./cmd/relay

linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-share-linux-arm64 ./cmd/share
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-relay-linux-arm64 ./cmd/relay

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -rf $(BIN)
