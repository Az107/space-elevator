.PHONY: build run test lint clean tidy install install-service

BINARY := space-elevator
DIST := dist
BIN := $(HOME)/.local/bin

build:
	go build -o $(DIST)/$(BINARY) ./cmd/space-elevator

run:
	go run ./cmd/space-elevator

tidy:
	go mod tidy

test:
	go test ./...

# Installs the binary. ~/.local/bin is used (the standard per-user
# location) because this tool runs rootless against the user's podman
# socket; /usr/bin would need root and buys nothing.
install: build
	install -m 0755 $(DIST)/$(BINARY) $(BIN)/$(BINARY)

# Installs the systemd user unit (rendered with the resolved bind address)
# and enables it. Run after `make install`.
install-service: install
	$(BIN)/$(BINARY) service install

clean:
	rm -rf $(DIST)