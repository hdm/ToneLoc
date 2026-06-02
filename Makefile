# ToneLoc/Go -- IPv4 war-dialer with a 90s MS-DOS vibe, powered by zmap-go.
#
# zmap-go is imported as a normal Go module; no sibling checkout needed.

GO      ?= go
BIN     ?= toneloc
PKG     := ./cmd/toneloc

.PHONY: all build test vet run web clean

all: build

build:
	$(GO) build -o $(BIN) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Quick local spin in the terminal (simulator backend).
run: build
	./$(BIN) 198.51.100.X /p:22,80,443

# Serve the ghostty.js web version on :8080.
web: build
	./$(BIN) 198.51.100.X /p:22,80,443 --web :8080

clean:
	rm -f $(BIN)
