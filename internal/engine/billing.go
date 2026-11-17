package engine

import (
	"net/http"
	"strings"
)

// BillingFrom reads the client's upstream credential header and returns the billing mode it
// implies (accountant design §7.1, Cortex spec §5.7). It reads only the credential's TYPE: it
// never returns, stores or logs any part of the credential itself (R2).
//
// Rule, in order:
//  1. "x-api-key" present, any value -> [BillingAPI]. An API key on this header is always an API
//     key, whatever it contains.
//  2. "Authorization: Bearer <token>":
//     - an Anthropic API key ("sk-ant-api...") or an OpenAI API key ("sk-..." that is not an
//     Anthropic OAuth token and is not a JWT, including the project-scoped "sk-proj-...")
//     -> [BillingAPI].
//     - an Anthropic OAuth token from a subscription login ("sk-ant-oat...") or a JWT access
//     token, as a ChatGPT login sends (three dot-separated base64url segments, RFC 7519 §3)
//     -> [BillingSubscription].
//     - anything else -> [BillingUnknown].
//  3. Any other scheme (for example "Authorization: Basic ..."), or no credential at all
//     -> [BillingUnknown].
//
// Header names are matched case-insensitively, as HTTP requires. [http.Header.Get] only
// canonicalizes the key it is asked for, not the keys already in the map — and a header map
// built from a non-Go source (an Envoy ext_proc header map, for example) may carry any case —
// so BillingFrom scans the map itself rather than relying on Get.
//
// Prefix sources: "sk-ant-api" and "sk-ant-oat" are Anthropic's own credential prefixes (the
// first for an API key, the second for Claude Code's OAuth access token on a subscription
// login); the bare "sk-" and "sk-proj-" prefixes are OpenAI's API key formats. A ChatGPT login
// sends a JWT, which carries none of those prefixes and is instead recognized by its three-part
// base64url shape (RFC 7519 §3). None of this is read from the token's payload — only its
// prefix or gross shape.
func BillingFrom(h http.Header) Billing {
	if headerGet(h, "X-Api-Key") != "" {
		return Billing{Mode: BillingAPI}
	}

	auth := headerGet(h, "Authorization")
	if auth == "" {
		return Billing{Mode: BillingUnknown}
	}

	scheme, token, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return Billing{Mode: BillingUnknown}
	}

	switch {
	case strings.HasPrefix(token, "sk-ant-oat"):
		return Billing{Mode: BillingSubscription}
	case strings.HasPrefix(token, "sk-ant-api"), strings.HasPrefix(token, "sk-"):
		return Billing{Mode: BillingAPI}
	case isJWTShape(token):
		return Billing{Mode: BillingSubscription}
	default:
		return Billing{Mode: BillingUnknown}
	}
}

// headerGet returns the first value of the named header, matching the name case-insensitively
// against every key already in h (see the case-insensitivity note on BillingFrom).
func headerGet(h http.Header, name string) string {
	for k, v := range h {
		if len(v) > 0 && strings.EqualFold(k, name) {
			return v[0]
		}
	}
	return ""
}

// isJWTShape reports whether s has the three dot-separated, non-empty base64url segments of a
// JWT (RFC 7519 §3). It checks shape only, never decodes or validates the token.
func isJWTShape(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || !isBase64URL(p) {
			return false
		}
	}
	return true
}

// isBase64URL reports whether s uses only the base64url alphabet (RFC 4648 §5), padding
// included. It does not require a valid length or decode the string.
func isBase64URL(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '=':
		default:
			return false
		}
	}
	return true
}
