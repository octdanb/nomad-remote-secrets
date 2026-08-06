# The binary name doubles as the Nomad provider name: jobs reference it with
# `provider = "onepassword"`, and Nomad looks for an executable of the same
# name under <common_plugin_dir>/secrets/.
BINARY  := onepassword
GOFLAGS := -trimpath -ldflags="-s -w"

.PHONY: build test lint install release clean

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -o bin/$(BINARY) .

test:
	go test ./...

lint:
	gofmt -l . | tee /dev/stderr | wc -l | grep -q '^0$$'
	go vet ./...

# Install into a Nomad client's plugin directory, e.g.
#   make install PLUGIN_DIR=/opt/nomad/plugins
PLUGIN_DIR ?= /opt/nomad/plugins
install: build
	install -d $(PLUGIN_DIR)/secrets
	install -m 0755 bin/$(BINARY) $(PLUGIN_DIR)/secrets/$(BINARY)

release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)_linux_amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o bin/$(BINARY)_linux_arm64 .

clean:
	rm -rf bin
