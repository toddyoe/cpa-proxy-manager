package main

// render.go holds the template rendering pipeline. It is intentionally free of
// host callbacks and network so every rule stays unit-testable: placeholders
// resolve custom-first with builtin fallback, an unresolvable placeholder or an
// invalid proxy URL makes the whole render fail (the account is then skipped).
import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var placeholderPattern = regexp.MustCompile(`\{(\w+)\}`)

const (
	builtinAccount  = "account"
	builtinEmail    = "email"
	builtinProvider = "provider"
)

// ValidateProxyURL reports whether s is a usable proxy address. The scheme set
// mirrors CPA proxyutil.Parse: http, https, socks5, socks5h.
func ValidateProxyURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return true
	default:
		return false
	}
}

// ValidateTemplateURL validates a proxy URL template. Placeholder braces are
// not legal URL characters for net/url, so each token is substituted with a
// dummy scalar before the full URL check. RenderTemplate still re-validates
// every rendered result, so credential values that break the URL are caught
// at render time.
func ValidateTemplateURL(tpl string) bool {
	tpl = strings.TrimSpace(tpl)
	if tpl == "" {
		return false
	}
	dummied := placeholderPattern.ReplaceAllString(tpl, "x")
	return ValidateProxyURL(dummied)
}

// jsonPathValue resolves a dot path (with optional leading "$.") against a
// decoded auth-file JSON object. Numeric segments index arrays. Only scalar
// leaves resolve; objects, arrays and null count as missing.
func jsonPathValue(doc map[string]any, path string) (string, bool) {
	cleaned := strings.TrimSpace(path)
	cleaned = strings.TrimPrefix(cleaned, "$.")
	cleaned = strings.TrimPrefix(cleaned, "$")
	if cleaned == "" {
		return "", false
	}
	var cur any = doc
	for _, seg := range strings.Split(cleaned, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return "", false
		}
		if m, ok := cur.(map[string]any); ok {
			cur = m[seg]
			continue
		}
		if arr, ok := cur.([]any); ok {
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(arr) {
				return "", false
			}
			cur = arr[idx]
			continue
		}
		return "", false
	}
	switch v := cur.(type) {
	case string:
		return v, v != ""
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}

// placeholderValue resolves one placeholder name against a credential.
// Custom placeholders win; builtins (account, email, provider) act as fallback.
func placeholderValue(name string, phs []Placeholder, cred *Credential) (string, bool) {
	for _, ph := range phs {
		if ph.Name == name {
			return jsonPathValue(cred.JSON, ph.Path)
		}
	}
	switch name {
	case builtinAccount:
		if cred.Email == "" {
			return "", false
		}
		if idx := strings.IndexByte(cred.Email, '@'); idx > 0 {
			return cred.Email[:idx], true
		}
		return cred.Email, true
	case builtinEmail:
		return cred.Email, cred.Email != ""
	case builtinProvider:
		return cred.Provider, cred.Provider != ""
	default:
		return "", false
	}
}

// RenderTemplate renders a proxy URL template for one credential. It returns
// an empty string when any placeholder is unresolvable or the produced URL is
// not a valid proxy address; callers treat that as "skip this account".
func RenderTemplate(urlTpl string, phs []Placeholder, cred *Credential) string {
	tpl := strings.TrimSpace(urlTpl)
	if tpl == "" || cred == nil {
		return ""
	}
	var missing bool
	out := placeholderPattern.ReplaceAllStringFunc(tpl, func(match string) string {
		name := match[1 : len(match)-1]
		value, ok := placeholderValue(name, phs, cred)
		if !ok {
			missing = true
			return match
		}
		return value
	})
	if missing {
		return ""
	}
	if !ValidateProxyURL(out) {
		return ""
	}
	return out
}

// renderPreview returns the rendered preview plus a machine-readable skip
// reason used by the credentials view.
func renderPreview(urlTpl string, phs []Placeholder, cred *Credential) (preview string, reason string) {
	rendered := RenderTemplate(urlTpl, phs, cred)
	if rendered != "" {
		return rendered, ""
	}
	if strings.TrimSpace(urlTpl) == "" {
		return "", "empty_template"
	}
	if missingPlaceholder(urlTpl, phs, cred) {
		return "", "placeholder_unresolvable"
	}
	return "", "invalid_url"
}

func missingPlaceholder(urlTpl string, phs []Placeholder, cred *Credential) bool {
	missing := false
	placeholderPattern.ReplaceAllStringFunc(urlTpl, func(match string) string {
		name := match[1 : len(match)-1]
		if _, ok := placeholderValue(name, phs, cred); !ok {
			missing = true
		}
		return match
	})
	return missing
}
