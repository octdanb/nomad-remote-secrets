# This is a single scheme-routed binary serving multiple backends
# (1Password, AWS Parameter Store, AWS Secrets Manager). The Nomad provider
# name is `secrets`: jobs reference it with `provider = "secrets"`, and the
# reference scheme (op://, aws-ssm:, aws-sm:) selects the backend at fetch
# time. Nomad discovers an executable named `secrets` under
# <common_plugin_dir>/secrets/.
#
# Back-compat: the binary dispatches on os.Args[1] (fetch/check/fingerprint),
# not on its own filename, so the same file installed under a second name
# works identically. The `install` target therefore also lays down an
# `onepassword` alias so existing jobs/clusters using provider = "onepassword"
# keep working unchanged.
BINARY  := secrets
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
	# Back-compat alias: the same binary served under the old provider name
	# so jobs/clusters using provider = "onepassword" keep resolving.
	ln -sf $(BINARY) $(PLUGIN_DIR)/secrets/onepassword

release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o bin/$(BINARY)_linux_amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o bin/$(BINARY)_linux_arm64 .

clean:
	rm -rf bin
