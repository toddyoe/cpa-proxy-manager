package main

// api_keys.go discovers and updates config-sourced API keys through the v8
// Management API. API key material is only parsed in this process to derive a
// stable identity digest; it is never returned to the page or persisted by the
// plugin.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

const apiKeyConfigPrefix = "/v8/management/config/api-keys/"

const (
	sourceOAuth  = "oauth"
	sourceAPIKey = "api_key"

	apiKeySourceConfig = "config"
	apiKeySourceFile   = "file"

	proxySourceCredential = "credential"
	proxySourceKey        = "key"
	proxySourceGroup      = "group"
	proxySourceGlobal     = "global"
)

// APIKeyTarget is the non-secret locator retained for a config-sourced key.
// FullHash covers the canonical identity tuple and Occurrence distinguishes
// otherwise identical duplicate entries.
type APIKeyTarget struct {
	ProviderFamily string
	FullHash       string
	Occurrence     int
}

var apiKeyProviderFamilies = []string{
	"gemini",
	"interactions",
	"vertex",
	"codex",
	"claude",
	"xai",
	"meta",
	"openai-compatibility",
}

func apiKeyIdentityHash(source, provider, name, baseURL, prefix, apiKey string) string {
	canonical, _ := json.Marshal([]string{
		"api_key",
		strings.TrimSpace(source),
		strings.TrimSpace(provider),
		strings.TrimSpace(name),
		strings.TrimSpace(baseURL),
		strings.TrimSpace(prefix),
		strings.TrimSpace(apiKey),
	})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func accountFromHash(fullHash string) string {
	if len(fullHash) < 8 {
		return fullHash
	}
	return fullHash[:8]
}

func safeAPIKeyContext(source, provider, name, baseURL, prefix, fullHash string) map[string]any {
	return map[string]any{
		"sha256":   fullHash,
		"account":  accountFromHash(fullHash),
		"provider": strings.TrimSpace(provider),
		"name":     strings.TrimSpace(name),
		"base-url": strings.TrimSpace(baseURL),
		"prefix":   strings.TrimSpace(prefix),
		"source":   strings.TrimSpace(source),
	}
}

func apiKeyProviderDisplay(family, groupName string) string {
	groupName = strings.TrimSpace(groupName)
	if family == "openai-compatibility" && groupName != "" {
		return family + "/" + groupName
	}
	return family
}

func decodeProviderGroups(raw []byte) ([]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode api-keys config: %w", err)
	}
	switch typed := value.(type) {
	case []any:
		return typed, nil
	case map[string]any:
		if items, ok := typed["items"].([]any); ok {
			return items, nil
		}
	}
	return nil, fmt.Errorf("decode api-keys config: expected a list")
}

func fetchAPIKeyProvider(ctx context.Context, family string, headers http.Header) ([]any, string, int, error) {
	path := apiKeyConfigPrefix + url.PathEscape(family)
	status, respHeaders, raw, err := callMgmtAPIWithHeaders(ctx, http.MethodGet, path, nil, headers, nil)
	if status == http.StatusNotFound {
		return nil, "", status, nil
	}
	if err != nil {
		return nil, "", status, err
	}
	groups, errDecode := decodeProviderGroups(raw)
	if errDecode != nil {
		return nil, "", status, errDecode
	}
	return groups, strings.TrimSpace(respHeaders.Get("ETag")), status, nil
}

func loadAPIKeyCredentials(headers http.Header) ([]Credential, []string) {
	type loadResult struct {
		creds   []Credential
		warning string
	}
	results := make(chan loadResult, len(apiKeyProviderFamilies))
	var wg sync.WaitGroup
	for _, family := range apiKeyProviderFamilies {
		family := family
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), mgmtRequestTimeout)
			defer cancel()
			groups, _, status, err := fetchAPIKeyProvider(ctx, family, headers)
			if err != nil {
				results <- loadResult{warning: fmt.Sprintf("%s: %v", family, err)}
				return
			}
			if status == http.StatusNotFound {
				results <- loadResult{}
				return
			}
			creds, errParse := apiKeyCredentialsFromGroups(family, groups)
			if errParse != nil {
				results <- loadResult{warning: fmt.Sprintf("%s: %v", family, errParse)}
				return
			}
			results <- loadResult{creds: creds}
		}()
	}
	wg.Wait()
	close(results)

	var creds []Credential
	var warnings []string
	for result := range results {
		creds = append(creds, result.creds...)
		if result.warning != "" {
			warnings = append(warnings, result.warning)
		}
	}
	sort.Slice(creds, func(i, j int) bool {
		if creds[i].Provider != creds[j].Provider {
			return creds[i].Provider < creds[j].Provider
		}
		if creds[i].Name != creds[j].Name {
			return creds[i].Name < creds[j].Name
		}
		return creds[i].TargetID < creds[j].TargetID
	})
	sort.Strings(warnings)
	return creds, warnings
}

func apiKeyCredentialsFromGroups(family string, groups []any) ([]Credential, error) {
	out := make([]Credential, 0)
	occurrences := map[string]int{}
	for groupIndex, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("group %d is not an object", groupIndex)
		}
		groupName, baseURL, groupPrefix, groupProxy := apiKeyGroupMetadata(group)
		keys, _ := group["keys"].([]any)
		for keyIndex, rawKey := range keys {
			key, ok := rawKey.(map[string]any)
			if !ok {
				continue
			}
			apiKey := strings.TrimSpace(stringValue(key, "api-key"))
			if apiKey == "" {
				continue
			}
			prefix := effectiveString(key, "prefix", groupPrefix)
			keyProxy, keyProxySet := optionalString(key, "proxy-url")
			fullHash := apiKeyIdentityHash(apiKeySourceConfig, family, groupName, baseURL, prefix, apiKey)
			occurrence := occurrences[fullHash]
			occurrences[fullHash]++
			name := apiKeyGroupDisplayName(family, groupName, groupIndex, keyIndex, len(keys))
			effectiveProxy := groupProxy
			proxySource := proxySourceGlobal
			if keyProxySet {
				effectiveProxy = keyProxy
				proxySource = proxySourceKey
			} else if strings.TrimSpace(groupProxy) != "" {
				proxySource = proxySourceGroup
			}
			out = append(out, Credential{
				TargetID:         fmt.Sprintf("apikey:config:%s:%s:%d", family, fullHash, occurrence),
				SourceKind:       sourceAPIKey,
				Name:             name,
				Provider:         apiKeyProviderDisplay(family, groupName),
				Account:          accountFromHash(fullHash),
				ProxyURL:         strings.TrimSpace(effectiveProxy),
				ProxyOverride:    strings.TrimSpace(keyProxy),
				ProxyOverrideSet: keyProxySet,
				ProxyKnown:       true,
				ProxySource:      proxySource,
				JSON:             safeAPIKeyContext(apiKeySourceConfig, family, groupName, baseURL, prefix, fullHash),
				APIKey: &APIKeyTarget{
					ProviderFamily: family,
					FullHash:       fullHash,
					Occurrence:     occurrence,
				},
				hydrated: true,
			})
		}
	}
	return out, nil
}

func apiKeyGroupDisplayName(family, groupName string, groupIndex, keyIndex, keyCount int) string {
	name := strings.TrimSpace(groupName)
	if name == "" {
		name = fmt.Sprintf("%s#%d", family, groupIndex+1)
	}
	if keyCount > 1 {
		return fmt.Sprintf("%s#%d", name, keyIndex+1)
	}
	return name
}

func apiKeyGroupMetadata(group map[string]any) (name, baseURL, prefix, proxyURL string) {
	return stringValue(group, "name"), stringValue(group, "base-url"), stringValue(group, "prefix"), stringValue(group, "proxy-url")
}

func stringValue(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func optionalString(values map[string]any, key string) (string, bool) {
	value, ok := values[key]
	if !ok || value == nil {
		return "", false
	}
	text, okString := value.(string)
	if !okString {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func effectiveString(values map[string]any, key, fallback string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return fallback
	}
	text, okString := value.(string)
	if !okString {
		return fallback
	}
	return strings.TrimSpace(text)
}

func fileAPIKeyCredential(entryName, provider string, doc map[string]any, entryAccount, entryBaseURL string) (account, fullHash, baseURL, prefix, apiKey string) {
	apiKey = strings.TrimSpace(docString(doc, "api_key"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(docString(doc, "api-key"))
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(entryAccount)
	}
	baseURL = strings.TrimSpace(docString(doc, "base_url"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(docString(doc, "base-url"))
	}
	if baseURL == "" {
		baseURL = strings.TrimSpace(entryBaseURL)
	}
	prefix = strings.TrimSpace(docString(doc, "prefix"))
	if apiKey == "" {
		return "", "", baseURL, prefix, ""
	}
	fullHash = apiKeyIdentityHash(apiKeySourceFile, provider, entryName, baseURL, prefix, apiKey)
	return accountFromHash(fullHash), fullHash, baseURL, prefix, apiKey
}

func docString(doc map[string]any, key string) string {
	value, _ := doc[key].(string)
	return value
}

func applyAPIKeyTargets(ctx context.Context, targets []EngineTarget, headers http.Header) error {
	if len(targets) == 0 {
		return nil
	}
	family := ""
	for _, target := range targets {
		if target.APIKey == nil {
			return fmt.Errorf("missing API key target")
		}
		if family == "" {
			family = target.APIKey.ProviderFamily
		} else if target.APIKey.ProviderFamily != family {
			return fmt.Errorf("mixed API key providers in one write")
		}
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		groups, etag, status, errGet := fetchAPIKeyProvider(ctx, family, headers)
		if errGet != nil {
			return errGet
		}
		if status == http.StatusNotFound {
			return fmt.Errorf("API Key 配置不存在")
		}
		if etag == "" {
			return fmt.Errorf("CPA 未返回配置 ETag，拒绝无并发保护的写入")
		}
		for _, target := range targets {
			key, errFind := findAPIKeyByTarget(groups, family, *target.APIKey)
			if errFind != nil {
				return errFind
			}
			if target.Value == "" {
				delete(key, "proxy-url")
			} else {
				key["proxy-url"] = target.Value
			}
		}
		stripInjectedAuthIndices(groups)
		body, errMarshal := json.Marshal(groups)
		if errMarshal != nil {
			return fmt.Errorf("encode api-keys config: %w", errMarshal)
		}
		extra := http.Header{"If-Match": []string{etag}}
		status, _, _, errPatch := callMgmtAPIWithHeaders(ctx, http.MethodPatch, apiKeyConfigPrefix+url.PathEscape(family), body, headers, extra)
		if status == http.StatusPreconditionFailed {
			lastErr = fmt.Errorf("配置已被其他操作修改，正在重试")
			continue
		}
		if errPatch != nil {
			return errPatch
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("配置写入冲突")
	}
	return lastErr
}

func findAPIKeyByTarget(groups []any, family string, target APIKeyTarget) (map[string]any, error) {
	seen := 0
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			continue
		}
		groupName, baseURL, groupPrefix, _ := apiKeyGroupMetadata(group)
		keys, _ := group["keys"].([]any)
		for _, rawKey := range keys {
			key, ok := rawKey.(map[string]any)
			if !ok {
				continue
			}
			apiKey := strings.TrimSpace(stringValue(key, "api-key"))
			if apiKey == "" {
				continue
			}
			prefix := effectiveString(key, "prefix", groupPrefix)
			fullHash := apiKeyIdentityHash(apiKeySourceConfig, family, groupName, baseURL, prefix, apiKey)
			if fullHash != target.FullHash {
				continue
			}
			if seen == target.Occurrence {
				return key, nil
			}
			seen++
		}
	}
	return nil, fmt.Errorf("API Key 目标已不存在或配置已变化")
}

func stripInjectedAuthIndices(groups []any) {
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			continue
		}
		delete(group, "auth_index")
		delete(group, "auth-index")
		keys, _ := group["keys"].([]any)
		for _, rawKey := range keys {
			key, ok := rawKey.(map[string]any)
			if !ok {
				continue
			}
			delete(key, "auth_index")
			delete(key, "auth-index")
		}
	}
}
