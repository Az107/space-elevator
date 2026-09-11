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

# Installs the systemd user unit and enables the service. Run after
# `make install` (and once after every build you want the service to
# pick up: systemctl --user restart space-elevator).
install-service: install
	mkdir -p $(HOME)/.config/systemd/user
	install -m 0644 deploy/$(BINARY).service $(HOME)/.config/systemd/user/$(BINARY).service
	systemctl --user daemon-reload
	systemctl --user enable --now $(BINARY).service

clean:
	rm -rf $(DIST)