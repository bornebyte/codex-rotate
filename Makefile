BINARY := codex-rotate
PREFIX ?= /usr/local/bin

.PHONY: build install uninstall fmt vet clean

build:
	go build -o $(BINARY) .

install: build
	install -m 0755 $(BINARY) $(PREFIX)/$(BINARY)

uninstall:
	rm -f $(PREFIX)/$(BINARY)

fmt:
	gofmt -l -w .

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
