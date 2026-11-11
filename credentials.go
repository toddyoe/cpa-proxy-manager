package main

// credentials.go builds the live credential inventory from host auth files and
// v8 API key configs. The table, preview, plan and apply paths share the same
// credentialMatches and filterCredentials functions so their scopes cannot
// diverge. Files are read only when hydration is needed, through the entry path
// because the plugin runs in-process with the host; host.auth.get remains the
// fallback for entries without a path. Nothing is cached between requests.
import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"cpa-proxy-manager/cpasdk/pluginapi"
)

// Credential is the plugin-side view of one auth entry plus its raw file JSON.
// JSON never leaves the plugin: the browser view only carries the pre-rendered
// preview and the current proxy value.
type Credential struct {
	TargetID         string
	SourceKind       string
	AuthIndex        string
	Name             string
	Provider         string
	Email            string
	Account          string
	ProxyURL         string
	ProxyOverride    string
	ProxyOverrideSet bool
	ProxyKnown       bool
	ProxySource      string
	Disabled         bool
	JSON             map[string]any
	APIKey           *APIKeyTarget
	entry            *pluginapi.HostAuthFileEntry
	hydrated         bool
}

// CredentialView is the JSON shape served to the page.
type CredentialView struct {
	TargetID    string `json:"target_id"`
	AuthIndex   string `json:"auth_index,omitempty"`
	SourceKind  string `json:"source_kind"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Email       string `json:"email,omitempty"`
	Account     string `json:"account,omitempty"`
	ProxyURL    string `json:"proxy_url"`
	ProxySource string `json:"proxy_source,omitempty"`
	ProxyKnown  bool   `json:"proxy_known"`
	Disabled    bool   `json:"disabled,omitempty"`
	Preview     string `json:"preview,omitempty"`
	Renderable  bool   `json:"renderable"`
	SkipReason  string `json:"skip_reason,omitempty"`
}

func callHostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, err := callHost("host.auth.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list result: %w", errUnmarshal)
	}
	return resp.Files, nil
}

func callHostAuthGet(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	raw, err := callHost("host.auth.get", pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if err != nil {
		return pluginapi.HostAuthGetResponse{}, err
	}
	var resp pluginapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host.auth.get result: %w", errUnmarshal)
	}
	return resp, nil
}

// PageFilter carries the query parameters shared by every data endpoint.
type PageFilter struct {
	Q        string
	Provider string
	Kind     string // "", "oauth" or "api_key"
	Bound    string // "", "bound" or "unbound"
	Page     int
	Size     int
}

const (
	defaultPageSize = 50
	maxPageSize     = 500
)

// ParsePageFilter reads and clamps the shared query parameters. Unknown or
// out-of-range values fall back to defaults instead of failing the request.
func ParsePageFilter(query url.Values) PageFilter {
	f := PageFilter{
		Q:        strings.TrimSpace(query.Get("q")),
		Provider: strings.TrimSpace(query.Get("provider")),
		Kind:     normalizeKind(query.Get("kind")),
		Bound:    normalizeBound(query.Get("bound")),
		Page:     1,
		Size:     defaultPageSize,
	}
	if v, err := strconv.Atoi(query.Get("page")); err == nil && v >= 1 {
		f.Page = v
	}
	if v, err := strconv.Atoi(query.Get("size")); err == nil && v >= 1 {
		f.Size = v
	}
	if f.Size > maxPageSize {
		f.Size = maxPageSize
	}
	return f
}

// normalizeBound accepts only the two explicit proxy-binding filters.
// Anything else, including empty, means "do not filter on binding".
func normalizeBound(v string) string {
	switch strings.TrimSpace(v) {
	case "bound", "unbound":
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func normalizeKind(v string) string {
	switch strings.TrimSpace(v) {
	case sourceOAuth, sourceAPIKey:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// PageSlice clamps page into range and returns that slice plus the adjusted
// page and total page count.
func PageSlice[T any](items []T, page, size int) (pageItems []T, adjusted int, pages int) {
	total := len(items)
	if size < 1 {
		size = 1
	}
	pages = (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return items[start:end], page, pages
}

// readAuthDoc returns the parsed auth file plus its root proxy_url. A direct
// disk read through the entry path keeps this local IO; host.auth.get is the
// fallback when the entry carries no path or the read fails.
func readAuthDoc(entry pluginapi.HostAuthFileEntry) (map[string]any, string, bool) {
	if path := strings.TrimSpace(entry.Path); path != "" {
		if raw, errRead := os.ReadFile(path); errRead == nil {
			doc := map[string]any{}
			_ = json.Unmarshal(raw, &doc)
			proxyURL, _ := doc["proxy_url"].(string)
			return doc, proxyURL, true
		}
	}
	resp, errGet := callHostAuthGet(entry.AuthIndex)
	if errGet != nil {
		return nil, "", false
	}
	doc := map[string]any{}
	_ = json.Unmarshal(resp.JSON, &doc)
	proxyURL, _ := doc["proxy_url"].(string)
	return doc, proxyURL, true
}

// hydrateOne builds the plugin-side credential view of one inventory entry.
func hydrateOne(entry pluginapi.HostAuthFileEntry) Credential {
	doc, proxyURL, known := readAuthDoc(entry)
	entryCopy := entry
	cred := Credential{
		TargetID:   "oauth:" + strings.TrimSpace(entry.AuthIndex),
		SourceKind: sourceOAuth,
		AuthIndex:  entry.AuthIndex,
		Name:       entry.Name,
		Provider:   entry.Provider,
		Email:      entry.Email,
		Account:    emailAccount(entry.Email),
		ProxyURL:   proxyURL,
		ProxyKnown: known,
		Disabled:   entry.Disabled,
		JSON:       doc,
		entry:      &entryCopy,
		hydrated:   true,
	}
	if known && strings.TrimSpace(proxyURL) != "" {
		cred.ProxyOverride = strings.TrimSpace(proxyURL)
		cred.ProxyOverrideSet = true
		cred.ProxySource = proxySourceCredential
	} else if known {
		cred.ProxySource = proxySourceGlobal
	}
	if isAPIKeyEntry(entry, doc) {
		cred.SourceKind = sourceAPIKey
		cred.TargetID = "apikey:file:" + strings.TrimSpace(entry.AuthIndex)
		account, fullHash, baseURL, prefix, _ := fileAPIKeyCredential(entry.Name, entry.Provider, doc, entry.Account, entry.BaseURL)
		if fullHash != "" {
			cred.Account = account
			cred.JSON = safeAPIKeyContext(apiKeySourceFile, entry.Provider, entry.Name, baseURL, prefix, fullHash)
			if cred.entry != nil {
				cred.entry.Account = ""
			}
		}
	}
	return cred
}

func emailAccount(email string) string {
	email = strings.TrimSpace(email)
	if idx := strings.IndexByte(email, '@'); idx > 0 {
		return email[:idx]
	}
	return email
}

func isAPIKeyEntry(entry pluginapi.HostAuthFileEntry, doc map[string]any) bool {
	kind := strings.ToLower(strings.TrimSpace(entry.AccountType))
	if kind == "api_key" || kind == "api-key" || kind == "apikey" {
		return true
	}
	if doc == nil {
		return false
	}
	for _, key := range []string{"api_key", "api-key"} {
		if value, ok := doc[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func oauthCredentialFromEntry(entry pluginapi.HostAuthFileEntry) Credential {
	entryCopy := entry
	return Credential{
		TargetID:   "oauth:" + strings.TrimSpace(entry.AuthIndex),
		SourceKind: sourceOAuth,
		AuthIndex:  entry.AuthIndex,
		Name:       entry.Name,
		Provider:   entry.Provider,
		Email:      entry.Email,
		Disabled:   entry.Disabled,
		entry:      &entryCopy,
	}
}

func materializeCredential(cred Credential) Credential {
	if cred.hydrated {
		return cred
	}
	if cred.entry != nil {
		return hydrateOne(*cred.entry)
	}
	cred.hydrated = true
	return cred
}

func materializeCredentials(creds []Credential) []Credential {
	out := make([]Credential, len(creds))
	var wg sync.WaitGroup
	sem := make(chan struct{}, fileReadConcurrency)
	for i := range creds {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[idx] = materializeCredential(creds[idx])
		}(i)
	}
	wg.Wait()
	return out
}

// fileReadConcurrency bounds parallel auth file reads.
const fileReadConcurrency = 32

func loadCredentialInventory(headers http.Header) ([]Credential, []string, error) {
	entries, errList := callHostAuthList()
	if errList != nil {
		return nil, nil, errList
	}
	creds := make([]Credential, 0, len(entries))
	for _, entry := range entries {
		if entry.RuntimeOnly || entry.AuthIndex == "" {
			continue
		}
		creds = append(creds, oauthCredentialFromEntry(entry))
	}
	apiKeyCreds, warnings := loadAPIKeyCredentials(headers)
	creds = append(creds, apiKeyCreds...)
	sort.Slice(creds, func(i, j int) bool {
		if creds[i].Provider != creds[j].Provider {
			return creds[i].Provider < creds[j].Provider
		}
		if creds[i].Name != creds[j].Name {
			return creds[i].Name < creds[j].Name
		}
		return creds[i].TargetID < creds[j].TargetID
	})
	return creds, warnings, nil
}

func credentialMatches(cred Credential, q, provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider != "" && cred.Provider != provider {
		return false
	}
	if q == "" {
		return true
	}
	needle := strings.ToLower(q)
	return strings.Contains(strings.ToLower(cred.Email), needle) ||
		strings.Contains(strings.ToLower(cred.Name), needle) ||
		strings.Contains(strings.ToLower(cred.Account), needle) ||
		strings.Contains(strings.ToLower(cred.Provider), needle)
}

func filterCredentials(creds []Credential, q, provider, kind, bound string) []Credential {
	bound = normalizeBound(bound)
	kind = normalizeKind(kind)
	candidates := make([]Credential, 0, len(creds))
	for _, cred := range creds {
		if credentialMatches(cred, q, provider) {
			candidates = append(candidates, cred)
		}
	}
	if kind == "" && bound == "" {
		return candidates
	}
	materialized := materializeCredentials(candidates)
	out := make([]Credential, 0, len(materialized))
	for _, cred := range materialized {
		if kind != "" && cred.SourceKind != kind {
			continue
		}
		if bound == "" {
			out = append(out, cred)
			continue
		}
		if !cred.ProxyKnown {
			continue
		}
		hasProxy := strings.TrimSpace(cred.ProxyURL) != ""
		if (bound == "bound" && hasProxy) || (bound == "unbound" && !hasProxy) {
			out = append(out, cred)
		}
	}
	return out
}

func distinctCredentialProviders(creds []Credential) []string {
	seen := make(map[string]struct{}, len(creds))
	out := make([]string, 0, len(creds))
	for _, cred := range creds {
		if cred.Provider == "" {
			continue
		}
		if _, dup := seen[cred.Provider]; dup {
			continue
		}
		seen[cred.Provider] = struct{}{}
		out = append(out, cred.Provider)
	}
	sort.Strings(out)
	return out
}

func credentialViews(creds []Credential, tpl Template, phs []Placeholder) []CredentialView {
	materialized := materializeCredentials(creds)
	out := make([]CredentialView, 0, len(materialized))
	for i := range materialized {
		cred := &materialized[i]
		view := CredentialView{
			TargetID:    cred.TargetID,
			AuthIndex:   cred.AuthIndex,
			SourceKind:  cred.SourceKind,
			Name:        cred.Name,
			Provider:    cred.Provider,
			Email:       cred.Email,
			Account:     cred.Account,
			ProxyURL:    cred.ProxyURL,
			ProxySource: cred.ProxySource,
			ProxyKnown:  cred.ProxyKnown,
			Disabled:    cred.Disabled,
		}
		view.Preview, view.SkipReason = renderPreview(tpl.URL, phs, cred)
		view.Renderable = view.Preview != ""
		out = append(out, view)
	}
	return out
}

func placeholderSamplesForCredential(cred Credential, phs []Placeholder) map[string]string {
	out := map[string]string{}
	if len(phs) == 0 {
		return out
	}
	cred = materializeCredential(cred)
	if cred.JSON == nil {
		return out
	}
	for _, ph := range phs {
		if value, ok := jsonPathValue(cred.JSON, ph.Path); ok {
			out[ph.Name] = value
		}
	}
	return out
}

// PlanCounts is the confirmation payload: how many entries the selection
// resolved to, how many the batch will write, and how many it will skip.
type PlanCounts struct {
	Matched int `json:"matched"`
	WillDo  int `json:"will_do"`
	Skipped int `json:"skipped"`
}

// batchRequest is the selection payload shared by the plan and apply
// endpoints. A non-empty Indexes picks an explicit selection (manual
// checkboxes); otherwise the shared filter scope minus Exclude applies (the
// select-all marker with per-row exceptions).
type batchRequest struct {
	Mode     string   `json:"mode"`
	Indexes  []string `json:"indexes"`
	Q        string   `json:"q"`
	Provider string   `json:"provider"`
	Kind     string   `json:"kind"`
	Bound    string   `json:"bound"`
	Exclude  []string `json:"exclude"`
}

// missingSelectionError reports explicitly requested indexes that no longer
// exist in the inventory.
type missingSelectionError struct {
	Indexes []string
}

func (e *missingSelectionError) Error() string {
	return T("err_unknown_target") + strings.Join(e.Indexes, ", ")
}

func resolveSelection(headers http.Header, req batchRequest) ([]Credential, []string, []string, error) {
	creds, warnings, errList := loadCredentialInventory(headers)
	if errList != nil {
		return nil, nil, nil, errList
	}
	var picked []Credential
	var missing []string
	if len(req.Indexes) > 0 {
		byID := make(map[string]Credential, len(creds))
		for _, cred := range creds {
			byID[cred.TargetID] = cred
		}
		for _, id := range req.Indexes {
			if cred, ok := byID[id]; ok {
				picked = append(picked, cred)
			} else {
				missing = append(missing, id)
			}
		}
	} else {
		excluded := make(map[string]struct{}, len(req.Exclude))
		for _, id := range req.Exclude {
			excluded[id] = struct{}{}
		}
		for _, cred := range filterCredentials(creds, req.Q, req.Provider, req.Kind, req.Bound) {
			if _, skip := excluded[cred.TargetID]; skip {
				continue
			}
			picked = append(picked, cred)
		}
	}
	return materializeCredentials(picked), warnings, missing, nil
}

func credentialLabel(cred Credential) string {
	if strings.TrimSpace(cred.Email) != "" {
		return cred.Email
	}
	if strings.TrimSpace(cred.Account) != "" {
		return cred.Account
	}
	return cred.Name
}

func targetsFromCredentials(mode string, creds []Credential, tpl Template, phs []Placeholder) (targets []EngineTarget, skipped int) {
	for i := range creds {
		cred := &creds[i]
		label := credentialLabel(*cred) + " · " + cred.Name
		preview, _ := renderPreview(tpl.URL, phs, cred)
		if cred.APIKey != nil {
			switch mode {
			case "set":
				if preview == "" {
					skipped++
					continue
				}
				targets = append(targets, EngineTarget{
					Kind:     targetKindAPIKeyConfig,
					TargetID: cred.TargetID,
					Value:    preview,
					Label:    label,
					APIKey:   cred.APIKey,
				})
			case "clear":
				if !cred.ProxyOverrideSet {
					skipped++
					continue
				}
				targets = append(targets, EngineTarget{
					Kind:     targetKindAPIKeyConfig,
					TargetID: cred.TargetID,
					Value:    "",
					Label:    label,
					APIKey:   cred.APIKey,
				})
			}
			continue
		}
		switch mode {
		case "set":
			if preview == "" {
				skipped++
				continue
			}
			targets = append(targets, EngineTarget{Kind: targetKindAuthFile, TargetID: cred.TargetID, FileName: cred.Name, Value: preview, Label: label})
		case "clear":
			if !cred.ProxyKnown || strings.TrimSpace(cred.ProxyURL) == "" {
				skipped++
				continue
			}
			targets = append(targets, EngineTarget{Kind: targetKindAuthFile, TargetID: cred.TargetID, FileName: cred.Name, Value: "", Label: label})
		}
	}
	return targets, skipped
}

// ResolveBatch computes the write set and confirmation counts for one batch
// request. The plan endpoint (confirmation numbers) and the apply endpoint
// (execution) both run this, so the numbers the user confirms are produced by
// the same code that picks the targets.
func ResolveBatch(headers http.Header, req batchRequest) ([]EngineTarget, PlanCounts, []string, error) {
	creds, warnings, missing, errSel := resolveSelection(headers, req)
	if errSel != nil {
		return nil, PlanCounts{}, nil, errSel
	}
	if len(missing) > 0 {
		return nil, PlanCounts{}, warnings, &missingSelectionError{Indexes: missing}
	}
	snapshot := configStore().Snapshot()
	tpl, _ := snapshot.Selected()
	targets, skipped := targetsFromCredentials(req.Mode, creds, tpl, snapshot.Placeholders)
	return targets, PlanCounts{Matched: len(creds), WillDo: len(targets), Skipped: skipped}, warnings, nil
}
