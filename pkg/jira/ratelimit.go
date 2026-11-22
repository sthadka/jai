package jira

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitInfo holds structured rate-limit telemetry parsed from Jira Cloud
// HTTP response headers (IETF RateLimit / Beta-RateLimit, RateLimit-Policy /
// Beta-RateLimit-Policy, X-RateLimit-NearLimit, X-RateLimit-Remaining,
// RateLimit-Reason, and Retry-After).
type RateLimitInfo struct {
	StatusCode int
	Method     string
	Path       string

	// Policy is the active policy identifier (e.g. "jira-quota-based",
	// "jira-burst-based", "jira-per-issue-on-write") from RateLimit or
	// RateLimit-Policy.
	Policy string

	// Remaining is the remaining points/tokens reported by the server, or -1 if
	// omitted (Jira omits r until remaining headroom falls below ~20%).
	Remaining int

	// ResetAfter is the duration until the current window resets (from ;t=).
	ResetAfter time.Duration

	// Quota is the total quota for the policy window (from ;q=), or -1 if omitted.
	Quota int

	// Window is the policy window length (from ;w=).
	Window time.Duration

	// NearLimit is true when X-RateLimit-NearLimit is "true" (<20% remaining).
	NearLimit bool

	// Reason is the raw RateLimit-Reason header (e.g. "jira-burst-based",
	// "jira-quota-based", "jira-per-issue-on-write").
	Reason string

	// RetryAfter is the parsed Retry-After duration (0 if absent).
	RetryAfter time.Duration

	// Timestamp is when the response was observed.
	Timestamp time.Time
}

// RateLimitObserver is invoked after every HTTP response received by Client,
// allowing callers (e.g. shared quota governors or diagnostics panels) to track
// headroom, near-limit transitions, and 429 reasons across requests.
// Implementations must be safe for concurrent use and non-blocking.
type RateLimitObserver func(RateLimitInfo)

// ClientOption configures a *Client constructed via New or NewWithClient.
type ClientOption func(*Client)

// WithRateLimiter overrides the client's default per-instance token-bucket
// limiter with a caller-supplied RateLimiter (e.g. a shared cross-client
// limiter or priority gate). A nil limiter is ignored.
func WithRateLimiter(l RateLimiter) ClientOption {
	return func(c *Client) {
		if l != nil {
			c.limiter = l
		}
	}
}

// WithRateLimitObserver registers a callback invoked on every HTTP response
// with parsed Jira Cloud rate-limit headers.
func WithRateLimitObserver(obs RateLimitObserver) ClientOption {
	return func(c *Client) {
		c.rlObserver = obs
	}
}

// ParseRateLimitHeaders extracts Jira Cloud rate-limit headers from h.
// Absent numeric values are reported as -1 (Remaining, Quota) or 0 (durations).
func ParseRateLimitHeaders(h http.Header) RateLimitInfo {
	info := RateLimitInfo{
		Remaining: -1,
		Quota:     -1,
	}
	if h == nil {
		return info
	}

	// RateLimit / Beta-RateLimit: "<policy>";r=<remaining>;t=<reset_secs>
	rlHeader := firstHeader(h, "RateLimit", "Beta-RateLimit")
	if rlHeader != "" {
		if policy, params := pickStructuredPolicy(rlHeader, "r"); policy != "" || len(params) > 0 {
			info.Policy = policy
			if v, ok := params["r"]; ok {
				if n, err := strconv.Atoi(v); err == nil {
					info.Remaining = n
				}
			}
			if v, ok := params["t"]; ok {
				if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
					info.ResetAfter = time.Duration(secs * float64(time.Second))
				}
			}
		}
	}

	// RateLimit-Policy / Beta-RateLimit-Policy: "<policy>";q=<quota>;w=<window_secs>
	rlPolicy := firstHeader(h, "RateLimit-Policy", "Beta-RateLimit-Policy")
	if rlPolicy != "" {
		if policy, params := pickStructuredPolicy(rlPolicy, "q"); policy != "" || len(params) > 0 {
			if info.Policy == "" {
				info.Policy = policy
			}
			if v, ok := params["q"]; ok {
				if n, err := strconv.Atoi(v); err == nil {
					info.Quota = n
				}
			}
			if v, ok := params["w"]; ok {
				if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
					info.Window = time.Duration(secs * float64(time.Second))
				}
			}
		}
	}

	// Fallback to legacy X-RateLimit-Remaining if RateLimit omitted ;r=.
	if info.Remaining < 0 {
		if rem := strings.TrimSpace(h.Get("X-RateLimit-Remaining")); rem != "" {
			if n, err := strconv.Atoi(rem); err == nil {
				info.Remaining = n
			}
		}
	}

	if near := strings.TrimSpace(h.Get("X-RateLimit-NearLimit")); strings.EqualFold(near, "true") {
		info.NearLimit = true
	}

	info.Reason = strings.TrimSpace(h.Get("RateLimit-Reason"))
	if info.Policy == "" && info.Reason != "" {
		info.Policy = info.Reason
	}

	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if d, err := parseRetryAfter(ra); err == nil {
			info.RetryAfter = d
		}
	}

	return info
}

func firstHeader(h http.Header, names ...string) string {
	for _, name := range names {
		if vals := h.Values(name); len(vals) > 0 {
			joined := strings.TrimSpace(strings.Join(vals, ", "))
			if joined != "" {
				return joined
			}
		}
	}
	return ""
}

// pickStructuredPolicy parses a comma-separated IETF structured-field list of
// `"policy";k=v;...` items. When multiple policies are present, prefers an item
// carrying preferParam (e.g. "r" or "q") with the smallest integer value for
// that parameter; otherwise falls back to the first well-formed item.
func pickStructuredPolicy(raw, preferParam string) (string, map[string]string) {
	items := splitOutsideQuotes(raw, ',')
	var bestPolicy string
	var bestParams map[string]string
	bestVal := math.MaxInt

	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.Split(item, ";")
		policy := strings.Trim(strings.TrimSpace(parts[0]), `"`)
		params := make(map[string]string, len(parts)-1)
		for _, p := range parts[1:] {
			k, v, ok := strings.Cut(p, "=")
			if !ok {
				continue
			}
			k = strings.ToLower(strings.TrimSpace(k))
			v = strings.Trim(strings.TrimSpace(v), `"`)
			if k != "" {
				params[k] = v
			}
		}
		if v, ok := params[preferParam]; ok {
			if n, err := strconv.Atoi(v); err == nil && n < bestVal {
				bestVal = n
				bestPolicy = policy
				bestParams = params
				continue
			}
		}
		if bestParams == nil {
			bestPolicy = policy
			bestParams = params
		}
	}
	return bestPolicy, bestParams
}

func splitOutsideQuotes(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			cur.WriteRune(r)
		case r == sep && !inQuotes:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// parseRetryAfter parses a Retry-After header value as either delay-seconds
// (integer or float) or an HTTP-date (RFC 9110). Negative results clamp to 0.
func parseRetryAfter(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if secs, err := strconv.ParseFloat(s, 64); err == nil {
		if secs <= 0 {
			return 0, nil
		}
		return time.Duration(secs * float64(time.Second)), nil
	}
	t, err := http.ParseTime(s)
	if err != nil {
		return 0, err
	}
	d := time.Until(t)
	if d < 0 {
		return 0, nil
	}
	return d, nil
}
