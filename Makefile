.PHONY: build run test lint clean tidy install

BINARY := space-elevator
DIST := dist

build:
	go build -o $(DIST)/$(BINARY) ./cmd/space-elevator

run:
	go run ./cmd/space-elevator

tidy:
	go mod tidy

test:
	go test ./...

install: build
	install -m 0755 $(DIST)/$(BINARY) $(HOME)/.local/bin/$(BINARY)

clean:
	rm -rf $(DIST)