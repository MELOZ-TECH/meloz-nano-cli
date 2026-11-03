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

# Com LIBKRUN=<clone compilado por build/krun/build-libkrun.sh>, o binário sai com
# o backend libkrun (-tags krun) e o tarball leva lib/libkrun.2.dylib, achada por
# rpath em @executable_path/../lib (o lib/ ao lado do bin/ no Homebrew), e os
# entitlements dos dois backends (build/krun.entitlements).
release:
ifeq ($(LIBKRUN),)
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .
	tar -czf $(BINARY)-$(VERSION)-darwin-arm64.tar.gz $(BINARY) $(ENTITLEMENTS) README.md LICENSE
else
	mkdir -p lib
	cp $(LIBKRUN)/target/release/libkrun.dylib lib/libkrun.2.dylib
	install_name_tool -id @rpath/libkrun.2.dylib lib/libkrun.2.dylib
	ln -sf libkrun.2.dylib lib/libkrun.dylib
	CGO_CFLAGS=-I$(LIBKRUN)/include CGO_LDFLAGS=-L$(CURDIR)/lib GOOS=darwin GOARCH=arm64 \
	  go build -tags krun -ldflags "$(LDFLAGS) -extldflags -Wl,-rpath,@executable_path/../lib" -o $(BINARY) .
	mkdir -p licenses && cp $(LIBKRUN)/LICENSE licenses/libkrun-LICENSE
	tar -czf $(BINARY)-$(VERSION)-darwin-arm64.tar.gz $(BINARY) lib/libkrun.2.dylib licenses/libkrun-LICENSE \
	  $(ENTITLEMENTS) build/krun.entitlements README.md LICENSE THIRD_PARTY.md
endif
	@echo "gerado $(BINARY)-$(VERSION)-darwin-arm64.tar.gz"

clean:
	rm -f $(BINARY) $(BINARY)-*.tar.gz lib/libkrun.2.dylib lib/libkrun.dylib licenses/libkrun-LICENSE
