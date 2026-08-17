package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cpa-proxy-manager/cpasdk/pluginapi"
)

func mgmtReq(method, path string, body any, headers http.Header) pluginapi.ManagementRequest {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	if headers == nil {
		headers = http.Header{}
	}
	return pluginapi.ManagementRequest{Method: method, Path: path, Headers: headers, Body: raw}
}

func TestManagementConfigRoundtrip(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())
	resp := dispatchManagement(mgmtReq(http.MethodGet, "/v0/management/plugins/cpa-proxy-manager/settings", nil, nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings status = %d, body = %s", resp.StatusCode, string(resp.Body))
	}
	var cfg Config
	if err := json.Unmarshal(resp.Body, &cfg); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	cfg.WriteConcurrency = 12
	resp = dispatchManagement(mgmtReq(http.MethodPut, "/v0/management/plugins/cpa-proxy-manager/settings", &cfg, nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /settings status = %d, body = %s", resp.StatusCode, string(resp.Body))
	}
	again := dispatchManagement(mgmtReq(http.MethodGet, "/v0/management/plugins/cpa-proxy-manager/settings", nil, nil))
	var reloaded Config
	_ = json.Unmarshal(again.Body, &reloaded)
	if reloaded.WriteConcurrency != 12 {
		t.Fatalf("roundtrip concurrency = %d, want 12", reloaded.WriteConcurrency)
	}
}

func TestManagementConfigRejectsInvalid(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())
	bad := map[string]any{"templates": []any{}, "selected_template": "x", "write_concurrency": 8}
	resp := dispatchManagement(mgmtReq(http.MethodPut, "/v0/management/plugins/cpa-proxy-manager/settings", bad, nil))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid settings status = %d, want 400", resp.StatusCode)
	}
}

func TestManagementApplyValidation(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())

	resp := dispatchManagement(mgmtReq(http.MethodPost, "/v0/management/plugins/cpa-proxy-manager/apply", map[string]any{"mode": "wrong"}, nil))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d, want 400", resp.StatusCode)
	}
	// A valid mode reaches the inventory and fails there without a host.
	resp = dispatchManagement(mgmtReq(http.MethodPost, "/v0/management/plugins/cpa-proxy-manager/apply", map[string]any{"mode": "set"}, nil))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("set without host status = %d, want 503", resp.StatusCode)
	}
	resp = dispatchManagement(mgmtReq(http.MethodPost, "/v0/management/plugins/cpa-proxy-manager/apply", map[string]any{"mode": "clear", "indexes": []string{"a"}}, nil))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("explicit selection without host status = %d, want 503", resp.StatusCode)
	}
}

func TestManagementPlanRejectsBadMode(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())
	resp := dispatchManagement(mgmtReq(http.MethodPost, "/v0/management/plugins/cpa-proxy-manager/plan", map[string]any{"mode": "nope"}, nil))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plan bad mode status = %d, want 400", resp.StatusCode)
	}
	resp = dispatchManagement(mgmtReq(http.MethodPost, "/v0/management/plugins/cpa-proxy-manager/plan", map[string]any{"mode": "set"}, nil))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("plan without host status = %d, want 503", resp.StatusCode)
	}
}

func TestManagementPreviewWithoutHostIs503(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())
	resp := dispatchManagement(mgmtReq(http.MethodGet, "/v0/management/plugins/cpa-proxy-manager/preview", nil, nil))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("preview without host status = %d, want 503", resp.StatusCode)
	}
}

func TestManagementCredentialsWithoutHostIs503(t *testing.T) {
	t.Setenv("CPA_PROXY_MANAGER_DATA_DIR", t.TempDir())
	resp := dispatchManagement(mgmtReq(http.MethodGet, "/v0/management/plugins/cpa-proxy-manager/credentials", nil, nil))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("credentials without host status = %d, want 503 (body: %s)", resp.StatusCode, string(resp.Body))
	}
}

func TestManagementResourceServesPage(t *testing.T) {
	resp := dispatchManagement(mgmtReq(http.MethodGet, "/v0/resource/plugins/cpa-proxy-manager/status", nil, nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resource status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "代理批量设置") {
		t.Fatal("resource page missing plugin title")
	}
}

func TestManagementUnknownRouteIs404(t *testing.T) {
	resp := dispatchManagement(mgmtReq(http.MethodGet, "/v0/management/plugins/cpa-proxy-manager/nope", nil, nil))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", resp.StatusCode)
	}
}
