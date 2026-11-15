package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// New (the default) must authenticate every request with HTTP Basic auth.
func TestNew_SendsBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		json.NewEncoder(w).Encode(MySelf{DisplayName: "u"})
	}))
	defer srv.Close()

	c := New(srv.URL, "me@example.com", "secret-token", 100)
	if _, err := c.MySelf(context.Background()); err != nil {
		t.Fatalf("MySelf: %v", err)
	}
	if !gotOK {
		t.Fatal("expected Basic auth header on request")
	}
	if gotUser != "me@example.com" || gotPass != "secret-token" {
		t.Errorf("basic auth = %q:%q, want me@example.com:secret-token", gotUser, gotPass)
	}
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// NewWithClient is the auth seam: a caller-supplied transport (e.g. an OAuth
// token source) must be used verbatim, with no Basic auth injected by jai.
func TestNewWithClient_UsesCustomTransport(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(MySelf{DisplayName: "u"})
	}))
	defer srv.Close()

	hc := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("Authorization", "Bearer oauth-access-token")
		return http.DefaultTransport.RoundTrip(r)
	})}

	c := NewWithClient(srv.URL, hc, 100)
	if _, err := c.MySelf(context.Background()); err != nil {
		t.Fatalf("MySelf: %v", err)
	}
	if gotAuth != "Bearer oauth-access-token" {
		t.Errorf("Authorization = %q, want injected bearer token", gotAuth)
	}
}
