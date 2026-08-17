package main

// mgmt.go is the loopback client for the CPA management API. The write channel
// is PATCH /v0/management/auth-files/fields: one call promotes the metadata,
// applies hot at runtime and persists the auth file. An empty proxy_url string
// clears the value; JSON null would be silently ignored by CPA's string type
// assertion, so callers must always pass a string.
import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const defaultMgmtBaseURL = "https://127.0.0.1:8317"

const mgmtRequestTimeout = 8 * time.Second

var managementCredentialCache = struct {
	mu  sync.RWMutex
	key string
}{}

func rememberManagementCredential(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	managementCredentialCache.mu.Lock()
	managementCredentialCache.key = key
	managementCredentialCache.mu.Unlock()
}

func cachedManagementCredential() string {
	managementCredentialCache.mu.RLock()
	defer managementCredentialCache.mu.RUnlock()
	return strings.TrimSpace(managementCredentialCache.key)
}

func envManagementPassword() string {
	for _, key := range []string{"CPA_PROXY_MANAGER_MANAGEMENT_PASSWORD", "MANAGEMENT_PASSWORD", "CPA_MANAGEMENT_KEY"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func extractBearerToken(headers http.Header) string {
	if headers == nil {
		return ""
	}
	auth := strings.TrimSpace(headers.Get("Authorization"))
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(auth)
}

// resolveManagementPassword prefers request headers, then the memory cache,
// then environment fallbacks for headless setups.
func resolveManagementPassword(headers http.Header) string {
	if headers != nil {
		if token := extractBearerToken(headers); token != "" {
			rememberManagementCredential(token)
			return token
		}
		if token := strings.TrimSpace(headers.Get("X-Management-Key")); token != "" {
			rememberManagementCredential(token)
			return token
		}
	}
	if key := cachedManagementCredential(); key != "" {
		return key
	}
	return envManagementPassword()
}

func configuredMgmtBaseURL() (string, bool) {
	for _, key := range []string{"CPA_PROXY_MANAGER_MGMT_BASE_URL", "CPA_MANAGEMENT_BASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return strings.TrimRight(v, "/"), true
		}
	}
	return "", false
}

func resolveMgmtBaseURL() string {
	if base, ok := configuredMgmtBaseURL(); ok {
		return base
	}
	return defaultMgmtBaseURL
}

func isLoopbackHost(host string) bool {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.Trim(hostname, "[]")
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

// mgmtHTTPClient picks a TLS config per request host: loopback https skips
// certificate verification so self-signed local deployments work; every other
// host verifies normally.
type mgmtTransport struct {
	base http.RoundTripper
}

func (t *mgmtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport := t.base
	if req.URL.Scheme == "https" && isLoopbackHost(req.URL.Host) {
		transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return transport.RoundTrip(req)
}

var mgmtClient = &http.Client{
	Timeout:   mgmtRequestTimeout,
	Transport: &mgmtTransport{base: http.DefaultTransport},
}

// MgmtHTTPDo is swappable for tests.
var mgmtHTTPDo = func(req *http.Request) (*http.Response, error) { return mgmtClient.Do(req) }

// ErrMgmtUnauthorized marks a failed write caused by a missing or rotated key.
var ErrMgmtUnauthorized = errors.New("管理密钥缺失或已失效，请刷新页面后重试")

func schemeFlipped(base string) string {
	if strings.HasPrefix(strings.ToLower(base), "http://") {
		return "https://" + base[len("http://"):]
	}
	if strings.HasPrefix(strings.ToLower(base), "https://") {
		return "http://" + base[len("https://"):]
	}
	return ""
}

func looksLikeSchemeMismatch(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "server gave http response to https client") ||
		strings.Contains(msg, "server gave https response to http client") ||
		strings.Contains(msg, "first record does not look like") ||
		strings.Contains(msg, "malformed http response")
}

func executeMgmtRequest(ctx context.Context, method, baseURL, path string, body []byte, password string) (int, []byte, error) {
	req, errRequest := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(body))
	if errRequest != nil {
		return 0, nil, errRequest
	}
	req.Header.Set("Authorization", "Bearer "+password)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, errDo := mgmtHTTPDo(req)
	if errDo != nil {
		return 0, nil, errDo
	}
	defer resp.Body.Close()
	raw, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return resp.StatusCode, nil, errRead
	}
	return resp.StatusCode, raw, nil
}

// callMgmtAPI dials the management API with one loopback scheme-flip retry so
// both TLS and plain-HTTP CPA deployments work out of the box.
func callMgmtAPI(ctx context.Context, method, path string, body []byte, headers http.Header) (int, []byte, error) {
	password := resolveManagementPassword(headers)
	if password == "" {
		return 0, nil, ErrMgmtUnauthorized
	}
	baseURL := resolveMgmtBaseURL()
	status, raw, err := executeMgmtRequest(ctx, method, baseURL, path, body, password)
	if err != nil {
		if _, explicit := configuredMgmtBaseURL(); !explicit {
			if alt := schemeFlipped(baseURL); alt != "" && isLoopbackHost(hostOf(baseURL)) && looksLikeSchemeMismatch(err) {
				if status2, raw2, err2 := executeMgmtRequest(ctx, method, alt, path, body, password); err2 == nil {
					return status2, raw2, nil
				}
			}
		}
		return status, raw, err
	}
	if status == http.StatusUnauthorized {
		return status, raw, ErrMgmtUnauthorized
	}
	if status < 200 || status >= 300 {
		return status, raw, fmt.Errorf("CPA 管理 API 返回 HTTP %d：%s", status, strings.TrimSpace(string(raw)))
	}
	return status, raw, nil
}

func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// PatchProxyURL writes (value != "") or clears (value == "") one credential's
// proxy_url through the management fields endpoint.
func PatchProxyURL(ctx context.Context, fileName, value string, headers http.Header) error {
	payload := map[string]string{
		"name":      fileName,
		"proxy_url": value,
	}
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return errMarshal
	}
	_, _, err := callMgmtAPI(ctx, http.MethodPatch, "/v0/management/auth-files/fields", body, headers)
	return err
}
