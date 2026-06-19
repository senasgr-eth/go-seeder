GO := /usr/local/go/bin/go
BINARY := multiseed

.PHONY: build clean vet install

build:
	$(GO) build -o $(BINARY) ./cmd/multiseeder/

clean:
	rm -f $(BINARY)

vet:
	$(GO) vet ./...

install: build
	sudo cp $(BINARY) /usr/local/bin/$(BINARY)
