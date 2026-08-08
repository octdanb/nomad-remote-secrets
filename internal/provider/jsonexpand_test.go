package provider

import "testing"

func TestExpandJSONObject(t *testing.T) {
	raw := `{"username":"app","password":"hunter2"}`
	values, obj := ExpandJSON(raw)
	if !obj {
		t.Fatal("Object = false, want true for a JSON object")
	}
	if values["username"] != "app" || values["password"] != "hunter2" {
		t.Errorf("keys not expanded: %v", values)
	}
	if values["value"] != raw {
		t.Errorf("value = %q, want the raw string", values["value"])
	}
}

func TestExpandJSONScalarArrayInvalid(t *testing.T) {
	for _, raw := range []string{`"just a string"`, `hunter2`, `[1,2,3]`, `{not json`, `42`} {
		values, obj := ExpandJSON(raw)
		if obj {
			t.Errorf("%q: Object = true, want false", raw)
		}
		if values["value"] != raw {
			t.Errorf("%q: value = %q", raw, values["value"])
		}
		if len(values) != 1 {
			t.Errorf("%q: expected only value key, got %v", raw, values)
		}
	}
}

func TestExpandJSONNestedStringified(t *testing.T) {
	raw := `{"db":{"host":"h","port":5432},"tags":["a","b"]}`
	values, obj := ExpandJSON(raw)
	if !obj {
		t.Fatal("Object = false, want true")
	}
	if values["db"] != `{"host":"h","port":5432}` {
		t.Errorf("nested object = %q, want compact JSON", values["db"])
	}
	if values["tags"] != `["a","b"]` {
		t.Errorf("nested array = %q, want compact JSON", values["tags"])
	}
}

func TestExpandJSONKeySanitization(t *testing.T) {
	raw := `{"my key!":"v","a.b.c":"w"}`
	values, _ := ExpandJSON(raw)
	if values["my_key"] != "v" {
		t.Errorf("my_key = %q", values["my_key"])
	}
	if values["a_b_c"] != "w" {
		t.Errorf("a_b_c = %q", values["a_b_c"])
	}
}

func TestExpandJSONNumbersAndBools(t *testing.T) {
	raw := `{"port":5432,"enabled":true,"ratio":1.5}`
	values, _ := ExpandJSON(raw)
	if values["port"] != "5432" {
		t.Errorf("port = %q, want 5432", values["port"])
	}
	if values["enabled"] != "true" {
		t.Errorf("enabled = %q, want true", values["enabled"])
	}
	if values["ratio"] != "1.5" {
		t.Errorf("ratio = %q, want 1.5", values["ratio"])
	}
}

func TestSanitizeKey(t *testing.T) {
	cases := map[string]string{
		"plain":     "plain",
		"my key":    "my_key",
		"a--b__c":   "a_b__c",
		"_leading_": "leading",
		"!!!":       "",
	}
	for in, want := range cases {
		if got := SanitizeKey(in); got != want {
			t.Errorf("SanitizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}
