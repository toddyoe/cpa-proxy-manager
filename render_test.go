package main

import "testing"

func testCredential() *Credential {
	return &Credential{
		AuthIndex: "idx-1",
		Name:      "0001.json",
		Provider:  "xai",
		Email:     "tom35@gmail.com",
		JSON: map[string]any{
			"type":              "oauth",
			"email":             "tom35@gmail.com",
			"account_id":        "acc-1001",
			"plan_type":         "plus",
			"region":            "us",
			"organization_uuid": "org-4242",
			"quota":             12.5,
			"verified":          true,
			"nested":            map[string]any{"inner": "value"},
			"members":           []any{"a", "b"},
		},
	}
}

func defaultPlaceholders() []Placeholder {
	return []Placeholder{
		{Name: "account_id", Path: "$.account_id"},
		{Name: "plan", Path: "$.plan_type"},
		{Name: "region", Path: "$.region"},
	}
}

func TestRenderTemplateBuiltins(t *testing.T) {
	cred := testCredential()
	cases := []struct{ tpl, want string }{
		{"http://Default.{account}:token@resin-host:2260", "http://Default.tom35:token@resin-host:2260"},
		{"http://{email}@resin-host:2260", "http://tom35@gmail.com@resin-host:2260"},
		{"socks5://{provider}.gw:1080", "socks5://xai.gw:1080"},
	}
	for _, tc := range cases {
		if got := RenderTemplate(tc.tpl, nil, cred); got != tc.want {
			t.Errorf("RenderTemplate(%q) = %q, want %q", tc.tpl, got, tc.want)
		}
	}
}

func TestRenderTemplateCustomPlaceholders(t *testing.T) {
	cred := testCredential()
	got := RenderTemplate("http://{plan}.{region}.gw:2260", defaultPlaceholders(), cred)
	if got != "http://plus.us.gw:2260" {
		t.Fatalf("custom placeholders render = %q", got)
	}
}

func TestRenderTemplateCustomOverridesBuiltin(t *testing.T) {
	cred := testCredential()
	phs := []Placeholder{{Name: "account", Path: "$.account_id"}}
	got := RenderTemplate("http://{account}@gw:2260", phs, cred)
	if got != "http://acc-1001@gw:2260" {
		t.Fatalf("custom account override = %q", got)
	}
}

func TestRenderTemplateSkips(t *testing.T) {
	cred := testCredential()
	cred.Email = "" // {account}/{email} become unresolvable
	cases := []string{
		"http://Default.{account}:token@resin-host:2260", // missing placeholder
		"ftp://{account}@resin-host:2260",                // unresolved + bad scheme
		"",                                               // empty template
	}
	for _, tpl := range cases {
		if got := RenderTemplate(tpl, defaultPlaceholders(), cred); got != "" {
			t.Errorf("RenderTemplate(%q) = %q, want empty", tpl, got)
		}
	}
	noEmailCred := testCredential()
	noEmailCred.Email = ""
	if got := RenderTemplate("http://{provider}.gw:2260", nil, noEmailCred); got != "http://xai.gw:2260" {
		t.Errorf("provider still resolves without email, got %q", got)
	}
}

func TestRenderTemplateInvalidURL(t *testing.T) {
	cred := testCredential()
	// Rendered result is not a URL at all.
	if got := RenderTemplate("{provider}", nil, cred); got != "" {
		t.Errorf("non-URL render = %q, want empty", got)
	}
}

func TestValidateProxyURL(t *testing.T) {
	valid := []string{
		"http://resin-host:2260",
		"https://Default.tom:secret@resin.example.com:2260",
		"socks5://gw:1080",
		"socks5h://gw:1080",
	}
	for _, u := range valid {
		if !ValidateProxyURL(u) {
			t.Errorf("ValidateProxyURL(%q) = false, want true", u)
		}
	}
	invalid := []string{"", "ftp://x:21", "resin-host:2260", "http://", "://nohost"}
	for _, u := range invalid {
		if ValidateProxyURL(u) {
			t.Errorf("ValidateProxyURL(%q) = true, want false", u)
		}
	}
}

func TestJSONPathValue(t *testing.T) {
	doc := testCredential().JSON
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"$.account_id", "acc-1001", true},
		{"account_id", "acc-1001", true},
		{"$.nested.inner", "value", true},
		{"$.members.0", "a", true},
		{"$.members.1", "b", true},
		{"$.quota", "12.5", true},
		{"$.verified", "true", true},
		{"$.nested", "", false},    // object is not a scalar
		{"$.members", "", false},   // array is not a scalar
		{"$.missing", "", false},   // absent key
		{"$.members.2", "", false}, // out of range
		{"$..double", "", false},   // unsupported wildcard
		{"", "", false},            // empty path
	}
	for _, tc := range cases {
		got, ok := jsonPathValue(doc, tc.path)
		if got != tc.want || ok != tc.ok {
			t.Errorf("jsonPathValue(%q) = (%q, %v), want (%q, %v)", tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRenderPreviewReasons(t *testing.T) {
	cred := testCredential()
	cred.Email = ""

	if _, reason := renderPreview("http://{account}@gw:2260", nil, cred); reason != "placeholder_unresolvable" {
		t.Errorf("skip reason = %q, want placeholder_unresolvable", reason)
	}
	if _, reason := renderPreview("", nil, cred); reason != "empty_template" {
		t.Errorf("skip reason = %q, want empty_template", reason)
	}
	if _, reason := renderPreview("{provider}", nil, cred); reason != "invalid_url" {
		t.Errorf("skip reason = %q, want invalid_url", reason)
	}
}
