// nomad-secret-plugin/onepassword is a Nomad secret provider plugin that
// resolves 1Password secret references (op://vault/item/field) through a
// 1Password Connect server.
//
// Nomad invokes the binary with an operation as the first argument:
//
//	onepassword fingerprint
//	onepassword fetch op://<vault>/<item>[/<section>]/<field>
//
// Install the binary as <client.common_plugin_dir>/secrets/onepassword on
// every Nomad client node; the executable's file name is the provider name
// used in job specifications.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/octdanb/nomad-secret-plugin/internal/plugin"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: onepassword <fingerprint|fetch|check> [path]")
	}

	switch os.Args[1] {
	case "fingerprint":
		if err := plugin.Fingerprint(os.Stdout); err != nil {
			fail(err.Error())
		}
	case "fetch":
		if len(os.Args) < 3 {
			fail("fetch requires a secret path, e.g. op://vault/item/field")
		}
		plugin.Fetch(os.Stdout, os.Stderr, os.Args[2])
	case "check":
		// Operator diagnostic, not called by Nomad: verifies this
		// node's config, connectivity, and (optionally) a reference.
		ref := ""
		if len(os.Args) > 2 {
			ref = os.Args[2]
		}
		os.Exit(plugin.Check(os.Stdout, ref))
	case "version", "--version", "-v":
		fmt.Println(plugin.Version)
	default:
		fail(fmt.Sprintf("unknown operation %q: expected fingerprint, fetch, or check", os.Args[1]))
	}
}

// fail reports an error in the JSON shape Nomad understands and exits
// non-zero. Used only for invocation errors; fetch-level failures are
// reported by plugin.Fetch with a zero exit.
func fail(msg string) {
	json.NewEncoder(os.Stdout).Encode(map[string]any{
		"result": map[string]string{},
		"error":  msg,
	})
	os.Exit(1)
}
