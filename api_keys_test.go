package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAPIKeyIdentityHashUsesFullTuple(t *testing.T) {
	base := apiKeyIdentityHash(apiKeySourceConfig, "claude", "main", "https://api.example.com", "team-a", "sk-test")
	if got := apiKeyIdentityHash(apiKeySourceConfig, "claude", "main", "https://api.example.com", "team-a", "sk-test"); got != base {
		t.Fatalf("stable hash mismatch: %s != %s", got, base)
	}
	if len(base) != 64 || len(accountFromHash(base)) != 8 {
		t.Fatalf("hash lengths = %d/%d", len(base), len(accountFromHash(base)))
	}
	cases := []string{
		apiKeyIdentityHash(apiKeySourceFile, "claude", "main", "https://api.example.com", "team-a", "sk-test"),
		apiKeyIdentityHash(apiKeySourceConfig, "codex", "main", "https://api.example.com", "team-a", "sk-test"),
		apiKeyIdentityHash(apiKeySourceConfig, "claude", "other", "https://api.example.com", "team-a", "sk-test"),
		apiKeyIdentityHash(apiKeySourceConfig, "claude", "main", "https://other.example.com", "team-a", "sk-test"),
		apiKeyIdentityHash(apiKeySourceConfig, "claude", "main", "https://api.example.com", "team-b", "sk-test"),
		apiKeyIdentityHash(apiKeySourceConfig, "claude", "main", "https://api.example.com", "team-a", "sk-other"),
	}
	for i, got := range cases {
		if got == base {
			t.Fatalf("identity field %d did not affect hash", i)
		}
	}
}

func TestAPIKeyCredentialsFromGroupsHonorsInheritanceAndHidesKey(t *testing.T) {
	groups := []any{
		map[string]any{
			"name":      "main",
			"base-url":  "https://api.example.com",
			"proxy-url": "http://group:8080",
			"keys": []any{
				map[string]any{"api-key": "sk-one"},
				map[string]any{"api-key": "sk-two", "proxy-url": "http://key:8080"},
			},
		},
	}
	creds, err := apiKeyCredentialsFromGroups("claude", groups)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("credentials = %d, want 2", len(creds))
	}
	if creds[0].ProxyURL != "http://group:8080" || creds[0].ProxySource != proxySourceGroup {
		t.Fatalf("first proxy = %q/%q", creds[0].ProxyURL, creds[0].ProxySource)
	}
	if creds[1].ProxyURL != "http://key:8080" || creds[1].ProxySource != proxySourceKey || !creds[1].ProxyOverrideSet {
		t.Fatalf("second proxy = %q/%q override=%v", creds[1].ProxyURL, creds[1].ProxySource, creds[1].ProxyOverrideSet)
	}
	if len(creds[0].Account) != 8 || creds[0].APIKey == nil {
		t.Fatalf("credential identity = %+v", creds[0])
	}
	if _, exists := creds[0].JSON["api-key"]; exists {
		t.Fatal("safe context leaked api-key")
	}
	if creds[0].JSON["sha256"] != creds[0].APIKey.FullHash {
		t.Fatalf("sha256 context = %v", creds[0].JSON["sha256"])
	}
	if creds[0].JSON["name"] != "main" {
		t.Fatalf("name context = %v, want configured group name", creds[0].JSON["name"])
	}
}

func TestApplyAPIKeyTargetsUsesETagAndPreservesGroup(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())
	var patchCalls atomic.Int32
	var gotIfMatch atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v8/management/config/api-keys/claude" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(`[{"name":"main","base-url":"https://api.example.com","auth_index":"group-auth","models":[{"name":"m","alias":"a"}],"keys":[{"api-key":"sk-one","weight":2,"auth-index":"key-auth"}]}]`))
		case http.MethodPatch:
			patchCalls.Add(1)
			gotIfMatch.Store(r.Header.Get("If-Match"))
			var groups []map[string]any
			if err := json.NewDecoder(r.Body).Decode(&groups); err != nil {
				t.Fatal(err)
			}
			keys := groups[0]["keys"].([]any)
			key := keys[0].(map[string]any)
			if key["proxy-url"] != "http://new:8080" {
				t.Fatalf("proxy-url = %v", key["proxy-url"])
			}
			if groups[0]["models"] == nil {
				t.Fatal("unrelated group fields were dropped")
			}
			if _, exists := groups[0]["auth_index"]; exists {
				t.Fatal("injected group auth_index was written back")
			}
			if _, exists := key["auth-index"]; exists {
				t.Fatal("injected key auth-index was written back")
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()
	t.Setenv("CPA_PROXY_MANAGER_MGMT_BASE_URL", server.URL)

	groups, errDecode := decodeProviderGroups([]byte(`[{"name":"main","base-url":"https://api.example.com","models":[{"name":"m","alias":"a"}],"keys":[{"api-key":"sk-one","weight":2}]}]`))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	creds, errCreds := apiKeyCredentialsFromGroups("claude", groups)
	if errCreds != nil || len(creds) != 1 {
		t.Fatalf("creds = %+v err=%v", creds, errCreds)
	}
	headers := http.Header{"Authorization": []string{"Bearer test-key"}}
	errApply := applyAPIKeyTargets(context.Background(), []EngineTarget{{
		Kind:   targetKindAPIKeyConfig,
		Label:  creds[0].Account,
		Value:  "http://new:8080",
		APIKey: creds[0].APIKey,
	}}, headers)
	if errApply != nil {
		t.Fatal(errApply)
	}
	if patchCalls.Load() != 1 || gotIfMatch.Load() != `"v1"` {
		t.Fatalf("patch calls=%d if-match=%v", patchCalls.Load(), gotIfMatch.Load())
	}
}

func TestSafeAPIKeyContextHasNoSecret(t *testing.T) {
	full := apiKeyIdentityHash(apiKeySourceConfig, "gemini", "g", "https://x", "", "secret-key")
	ctx := safeAPIKeyContext(apiKeySourceConfig, "gemini", "g", "https://x", "", full)
	raw, _ := json.Marshal(ctx)
	if strings.Contains(string(raw), "secret-key") {
		t.Fatalf("safe context leaked key: %s", raw)
	}
	if ctx["sha256"] != full || ctx["account"] != full[:8] {
		t.Fatalf("context = %#v", ctx)
	}
}

func TestAPIKeyIdentityHashFixedContract(t *testing.T) {
	got := apiKeyIdentityHash(apiKeySourceConfig, "claude", "main", "https://api.example.com", "team-a", "sk-test")
	const want = "76018f25fd575ab21158979d366a9dbeedbb2a9c88f03709c3379bc6573acb26"
	if got != want {
		t.Fatalf("identity hash = %s, want %s", got, want)
	}
}

func TestMgmtAPIErrorOmitsResponseBody(t *testing.T) {
	const secret = "sk-secret-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"api-key":"` + secret + `"}`))
	}))
	defer server.Close()
	t.Setenv("CPA_PROXY_MANAGER_MGMT_BASE_URL", server.URL)
	headers := http.Header{"Authorization": []string{"Bearer test-key"}}
	status, _, raw, err := callMgmtAPIWithHeaders(context.Background(), http.MethodGet, "/v8/management/config/api-keys/claude", nil, headers, nil)
	if err == nil || status != http.StatusInternalServerError {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Fatal("test response did not contain the expected secret fixture")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("management error leaked response body: %v", err)
	}
}

func TestAPIKeyOccurrenceDistinguishesDuplicates(t *testing.T) {
	groups := []any{
		map[string]any{"name": "main", "base-url": "https://api.example.com", "keys": []any{map[string]any{"api-key": "sk-same"}}},
		map[string]any{"name": "main", "base-url": "https://api.example.com", "keys": []any{map[string]any{"api-key": "sk-same", "marker": "second"}}},
	}
	creds, err := apiKeyCredentialsFromGroups("claude", groups)
	if err != nil || len(creds) != 2 {
		t.Fatalf("creds=%+v err=%v", creds, err)
	}
	if creds[0].APIKey.FullHash != creds[1].APIKey.FullHash || creds[0].APIKey.Occurrence != 0 || creds[1].APIKey.Occurrence != 1 {
		t.Fatalf("occurrences = %+v / %+v", creds[0].APIKey, creds[1].APIKey)
	}
	if creds[0].TargetID == creds[1].TargetID {
		t.Fatalf("duplicate target IDs = %q", creds[0].TargetID)
	}
	found, errFind := findAPIKeyByTarget(groups, "claude", *creds[1].APIKey)
	if errFind != nil || found["marker"] != "second" {
		t.Fatalf("found=%v err=%v", found, errFind)
	}
}

func TestAPIKeyWriteRetriesAfterETagConflict(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())
	var getCalls atomic.Int32
	var patchCalls atomic.Int32
	var firstIfMatch atomic.Value
	var secondIfMatch atomic.Value
	const fixture = `[{"name":"main","base-url":"https://api.example.com","keys":[{"api-key":"sk-one"}]}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			n := getCalls.Add(1)
			if n == 1 {
				w.Header().Set("ETag", `"v1"`)
			} else {
				w.Header().Set("ETag", `"v2"`)
			}
			_, _ = w.Write([]byte(fixture))
		case http.MethodPatch:
			n := patchCalls.Add(1)
			if n == 1 {
				firstIfMatch.Store(r.Header.Get("If-Match"))
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			secondIfMatch.Store(r.Header.Get("If-Match"))
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()
	t.Setenv("CPA_PROXY_MANAGER_MGMT_BASE_URL", server.URL)
	groups, errDecode := decodeProviderGroups([]byte(fixture))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	creds, errCreds := apiKeyCredentialsFromGroups("claude", groups)
	if errCreds != nil || len(creds) != 1 {
		t.Fatalf("creds=%+v err=%v", creds, errCreds)
	}
	headers := http.Header{"Authorization": []string{"Bearer test-key"}}
	errApply := applyAPIKeyTargets(context.Background(), []EngineTarget{{
		Kind:   targetKindAPIKeyConfig,
		Label:  creds[0].Account,
		Value:  "http://new:8080",
		APIKey: creds[0].APIKey,
	}}, headers)
	if errApply != nil {
		t.Fatal(errApply)
	}
	if getCalls.Load() != 2 || patchCalls.Load() != 2 {
		t.Fatalf("GETs=%d PATCHes=%d, want 2/2", getCalls.Load(), patchCalls.Load())
	}
	if firstIfMatch.Load() != `"v1"` || secondIfMatch.Load() != `"v2"` {
		t.Fatalf("If-Match values = %v / %v", firstIfMatch.Load(), secondIfMatch.Load())
	}
}
