package onepassword

import "testing"

func TestCacheScopeSeparatesBackends(t *testing.T) {
	connect := (&Provider{cfg: Config{ConnectHost: "http://c:8080", Token: "tok"}}).cacheScope()
	sa := (&Provider{cfg: Config{ConnectHost: "http://c:8080", Token: "tok", ServiceAccountToken: "ops_x"}}).cacheScope()
	if connect == sa {
		t.Error("connect and service-account configs must not share cache entries")
	}
	sa2 := (&Provider{cfg: Config{ServiceAccountToken: "ops_y"}}).cacheScope()
	if sa == sa2 {
		t.Error("different service account tokens must not share cache entries")
	}
}
