package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestBillingFrom pins the header-to-mode table of accountant design §7.1 and Cortex spec §5.7.
// Exact prefixes:
//   - "sk-ant-api" (Anthropic API key, e.g. sk-ant-api03-...) and the generic OpenAI "sk-" /
//     "sk-proj-" prefixes are documented at https://docs.anthropic.com/en/api/getting-started and
//     https://platform.openai.com/docs/api-reference/authentication.
//   - "sk-ant-oat" is the prefix Claude Code's OAuth access token uses for a subscription login
//     (observed in Claude Code's own credential files; also named in Cortex spec §5.7).
//   - A ChatGPT login sends a JWT access token: three dot-separated base64url segments
//     (RFC 7519 §3), which carries no recognizable "sk-" prefix.
func TestBillingFrom(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJl"

	cases := []struct {
		name string
		h    http.Header
		want BillingMode
	}{
		{
			name: "x-api-key present (Anthropic API key) -> api",
			h:    http.Header{"X-Api-Key": []string{"sk-ant-api03-abcdef"}},
			want: BillingAPI,
		},
		{
			name: "x-api-key present with any value -> api",
			h:    http.Header{"X-Api-Key": []string{"anything-at-all"}},
			want: BillingAPI,
		},
		{
			name: "Authorization Bearer Anthropic API key -> api",
			h:    http.Header{"Authorization": []string{"Bearer sk-ant-api03-abcdef"}},
			want: BillingAPI,
		},
		{
			name: "Authorization Bearer Anthropic OAuth token (subscription login) -> subscription",
			h:    http.Header{"Authorization": []string{"Bearer sk-ant-oat01-abcdef"}},
			want: BillingSubscription,
		},
		{
			name: "Authorization Bearer OpenAI project key -> api",
			h:    http.Header{"Authorization": []string{"Bearer sk-proj-abcdef"}},
			want: BillingAPI,
		},
		{
			name: "Authorization Bearer OpenAI key -> api",
			h:    http.Header{"Authorization": []string{"Bearer sk-abcdef123456"}},
			want: BillingAPI,
		},
		{
			name: "Authorization Bearer JWT (ChatGPT login) -> subscription",
			h:    http.Header{"Authorization": []string{"Bearer " + jwt}},
			want: BillingSubscription,
		},
		{
			name: "Authorization Basic -> unknown",
			h:    http.Header{"Authorization": []string{"Basic dXNlcjpwYXNz"}},
			want: BillingUnknown,
		},
		{
			name: "no credential -> unknown",
			h:    http.Header{},
			want: BillingUnknown,
		},
		{
			name: "header names are case-insensitive (x-api-key)",
			h:    http.Header{"X-API-KEY": []string{"sk-ant-api03-abcdef"}},
			want: BillingAPI,
		},
		{
			name: "header names are case-insensitive (authorization)",
			h:    http.Header{"authorization": []string{"Bearer sk-ant-oat01-abcdef"}},
			want: BillingSubscription,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BillingFrom(c.h)
			if got.Mode != c.want {
				t.Fatalf("BillingFrom(%v) = %q, want %q", c.h, got.Mode, c.want)
			}
		})
	}
}

// TestBillingFrom_NeverReturnsCredential pins accountant R2: the returned Billing carries no part
// of any credential the request presented, in any field reachable by JSON encoding.
func TestBillingFrom_NeverReturnsCredential(t *testing.T) {
	tokens := []struct {
		header, value string
	}{
		{"X-Api-Key", "sk-ant-api03-SECRET-KEY-VALUE"},
		{"Authorization", "Bearer sk-ant-oat01-SECRET-OAUTH-VALUE"},
		{"Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJTRUNSRVRfU1VCIn0.c2lnbmF0dXJl"},
		{"Authorization", "Basic U0VDUkVUX0JBU0ljCg=="},
	}

	for _, tok := range tokens {
		h := http.Header{tok.header: []string{tok.value}}
		b := BillingFrom(h)

		out, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		// Look for the raw value and any of its base64url/base64-ish substrings of length >= 8.
		raw := tok.value
		for i := 0; i+8 <= len(raw); i++ {
			substr := raw[i : i+8]
			if strings.Contains(string(out), substr) {
				t.Fatalf("Billing JSON %s leaked a substring of the credential %q: %q", out, raw, substr)
			}
		}
	}
}
