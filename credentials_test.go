package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"cpa-proxy-manager/cpasdk/pluginapi"
)

func TestCredentialMatches(t *testing.T) {
	cases := []struct {
		name     string
		cred     Credential
		q        string
		provider string
		want     bool
	}{
		{"empty filter matches all", Credential{Name: "a.json"}, "", "", true},
		{"provider exact match", Credential{Name: "a.json", Provider: "claude"}, "", "claude", true},
		{"provider mismatch", Credential{Name: "a.json", Provider: "claude"}, "", "gemini", false},
		{"q matches email case-insensitively", Credential{Name: "a.json", Email: "user@example.com"}, "user@", "", true},
		{"q matches name", Credential{Name: "team-a.json"}, "team-a", "", true},
		{"q matches account digest", Credential{Name: "a.json", Account: "3f2a1b7c"}, "3f2a", "", true},
		{"q matches nothing", Credential{Name: "a.json", Email: "user@example.com"}, "zzz", "", false},
		{"empty email never matches q", Credential{Name: "a.json"}, "x", "", false},
		{"q and provider both apply", Credential{Name: "a.json", Provider: "claude", Email: "user@example.com"}, "u@", "gemini", false},
	}
	for _, tc := range cases {
		if got := credentialMatches(tc.cred, tc.q, tc.provider); got != tc.want {
			t.Errorf("%s: credentialMatches(%q, %q) = %v, want %v", tc.name, tc.q, tc.provider, got, tc.want)
		}
	}
}

func TestParsePageFilter(t *testing.T) {
	f := ParsePageFilter(url.Values{})
	if f.Page != 1 || f.Size != defaultPageSize {
		t.Fatalf("defaults: page=%d size=%d, want 1/%d", f.Page, f.Size, defaultPageSize)
	}
	f = ParsePageFilter(url.Values{"page": {"3"}, "size": {"100"}, "q": {" alice "}, "provider": {"claude"}, "bound": {" bound "}})
	if f.Page != 3 || f.Size != 100 || f.Q != "alice" || f.Provider != "claude" || f.Bound != "bound" {
		t.Fatalf("parsed: %+v", f)
	}
	f = ParsePageFilter(url.Values{"page": {"0"}, "size": {"9999"}, "bound": {"nope"}})
	if f.Page != 1 || f.Size != maxPageSize || f.Bound != "" {
		t.Fatalf("clamped: page=%d size=%d bound=%q, want 1/%d empty", f.Page, f.Size, f.Bound, maxPageSize)
	}
}

func TestFilterCredentialsByBound(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	entries := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "bound", Name: "bound.json", Provider: "claude", Path: write("bound.json", `{"proxy_url":"http://127.0.0.1:1"}`)},
		{AuthIndex: "empty", Name: "empty.json", Provider: "claude", Path: write("empty.json", `{"proxy_url":""}`)},
		{AuthIndex: "blank", Name: "blank.json", Provider: "claude", Path: write("blank.json", `{"proxy_url":"  "}`)},
		{AuthIndex: "missing-key", Name: "nokey.json", Provider: "claude", Path: write("nokey.json", `{}`)},
		{AuthIndex: "number", Name: "number.json", Provider: "claude", Path: write("number.json", `{"proxy_url":1}`)},
		{AuthIndex: "unread", Name: "unread.json", Provider: "claude", Path: filepath.Join(dir, "no-such.json")},
		{AuthIndex: "gemini", Name: "gemini.json", Provider: "gemini", Path: write("gemini.json", `{"proxy_url":"socks5://127.0.0.1:2"}`)},
	}
	creds := make([]Credential, len(entries))
	for i, entry := range entries {
		creds[i] = oauthCredentialFromEntry(entry)
	}
	ids := func(got []Credential) []string {
		out := make([]string, len(got))
		for i, cred := range got {
			out[i] = cred.AuthIndex
		}
		return out
	}
	if got := ids(filterCredentials(creds, "", "claude", "", "bound")); len(got) != 1 || got[0] != "bound" {
		t.Fatalf("bound claude: %v", got)
	}
	if got := ids(filterCredentials(creds, "", "claude", "", "unbound")); len(got) != 4 || got[0] != "empty" || got[1] != "blank" || got[2] != "missing-key" || got[3] != "number" {
		t.Fatalf("unbound claude: %v", got)
	}
	if got := ids(filterCredentials(creds, "", "gemini", "", "bound")); len(got) != 1 || got[0] != "gemini" {
		t.Fatalf("bound gemini: %v", got)
	}
	if got := ids(filterCredentials(creds, "", "", "", "")); len(got) != 7 || got[0] != "bound" || got[6] != "gemini" {
		t.Fatalf("no binding filter: %v", got)
	}
}

func TestPageSlice(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	got, page, pages := PageSlice(items, 1, 2)
	if len(got) != 2 || got[0] != 1 || page != 1 || pages != 3 {
		t.Fatalf("first page: %v page=%d pages=%d", got, page, pages)
	}
	got, page, pages = PageSlice(items, 99, 2)
	if len(got) != 1 || got[0] != 5 || page != 3 || pages != 3 {
		t.Fatalf("overflow page clamps to last: %v page=%d pages=%d", got, page, pages)
	}
	_, page, pages = PageSlice([]int(nil), 5, 50)
	if page != 1 || pages != 1 {
		t.Fatalf("empty slice: page=%d pages=%d, want 1/1", page, pages)
	}
}

func TestFilterCredentialsMaterializesAPIKeyKind(t *testing.T) {
	dir := t.TempDir()
	apiKeyPath := filepath.Join(dir, "api-key.json")
	if err := os.WriteFile(apiKeyPath, []byte(`{"api_key":"sk-file-test","base_url":"https://api.example.com"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	oauthPath := filepath.Join(dir, "oauth.json")
	if err := os.WriteFile(oauthPath, []byte(`{"type":"oauth","email":"user@example.com"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	creds := []Credential{
		oauthCredentialFromEntry(pluginapi.HostAuthFileEntry{AuthIndex: "api", Name: "api-key.json", Provider: "claude", AccountType: "api_key", Path: apiKeyPath}),
		oauthCredentialFromEntry(pluginapi.HostAuthFileEntry{AuthIndex: "oauth", Name: "oauth.json", Provider: "claude", Path: oauthPath}),
	}
	got := filterCredentials(creds, "", "", sourceAPIKey, "")
	if len(got) != 1 || got[0].AuthIndex != "api" {
		t.Fatalf("API key filter = %+v", got)
	}
	if got[0].SourceKind != sourceAPIKey || len(got[0].Account) != 8 {
		t.Fatalf("materialized API key = %+v", got[0])
	}
	if _, exists := got[0].JSON["api_key"]; exists {
		t.Fatal("API key file context leaked the raw API key")
	}
}
