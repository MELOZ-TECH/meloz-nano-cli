BINARY := nanoc
ENTITLEMENTS := build/virt.entitlements
DOCKER_HOST_ENV := unix://$(HOME)/.nanoc/docker.sock
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: bootstrap build sign test itest run release clean

bootstrap:
	./scripts/bootstrap.sh

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

sign: build
	codesign --force --sign - --entitlements $(ENTITLEMENTS) $(BINARY)

test:
	go vet ./...
	go test ./...

itest: sign
	./scripts/integration-test.sh

run: sign
	./$(BINARY) up
	@echo "DOCKER_HOST=$(DOCKER_HOST_ENV)"

release:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .
	tar -czf $(BINARY)-$(VERSION)-darwin-arm64.tar.gz $(BINARY) $(ENTITLEMENTS) README.md LICENSE
	@echo "gerado $(BINARY)-$(VERSION)-darwin-arm64.tar.gz"

clean:
	rm -f $(BINARY) $(BINARY)-*.tar.gz
