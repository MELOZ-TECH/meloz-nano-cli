BINARY := nano
ENTITLEMENTS := build/virt.entitlements
DOCKER_HOST_ENV := unix://$(HOME)/.nano/docker.sock

.PHONY: bootstrap build sign test itest run clean

bootstrap:
	./scripts/bootstrap.sh

build:
	go build -o $(BINARY) .

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

clean:
	rm -f $(BINARY)
