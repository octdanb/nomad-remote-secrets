# This is a single scheme-routed binary serving multiple backends
# (1Password, AWS Parameter Store, AWS Secrets Manager). The Nomad provider
# name is `remote-secrets`: jobs reference it with `provider = "remote-secrets"`,
# and the reference scheme (op://, aws-ssm:, aws-sm:) selects the backend at
# fetch time. Nomad discovers an executable named `remote-secrets` under
# <common_plugin_dir>/secrets/ (the "secrets" dir is Nomad's plugin type dir).
BINARY  := remote-secrets
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
