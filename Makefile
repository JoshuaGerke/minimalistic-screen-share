# minimalistic-screen-share – baut die App.
# Auf Windows lieber build.ps1 nehmen.

GO ?= go
BIN := bin
LDFLAGS := -s -w

.PHONY: all clean test vet fmt windows linux linux-arm

all: share

share:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-share ./cmd/share

windows:
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS) -H=windowsgui" -o $(BIN)/mss-share.exe ./cmd/share

linux: linux-amd64
linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-share-linux-amd64 ./cmd/share

linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/mss-share-linux-arm64 ./cmd/share

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -rf $(BIN)
