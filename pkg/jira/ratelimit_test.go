package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseRateLimitHeaders(t *testing.T) {
	t.Run("structured RateLimit and RateLimit-Policy", func(t *testing.T) {
		h := http.Header{}
		h.Set("RateLimit", `"jira-quota-based";r=1200;t=45`)
		h.Set("RateLimit-Policy", `"jira-quota-based";q=65000;w=3600`)
		h.Set("X-RateLimit-NearLimit", "true")
		h.Set("RateLimit-Reason", "jira-quota-based")
		h.Set("Retry-After", "12")

		info := ParseRateLimitHeaders(h)
		if info.Policy != "jira-quota-based" {
			t.Errorf("Policy = %q, want jira-quota-based", info.Policy)
		}
		if info.Remaining != 1200 {
			t.Errorf("Remaining = %d, want 1200", info.Remaining)
		}
		if info.ResetAfter != 45*time.Second {
			t.Errorf("ResetAfter = %v, want 45s", info.ResetAfter)
		}
		if info.Quota != 65000 {
			t.Errorf("Quota = %d, want 65000", info.Quota)
		}
		if info.Window != 3600*time.Second {
			t.Errorf("Window = %v, want 1h", info.Window)
		}
		if !info.NearLimit {
			t.Error("expected NearLimit=true")
		}
		if info.Reason != "jira-quota-based" {
			t.Errorf("Reason = %q, want jira-quota-based", info.Reason)
		}
		if info.RetryAfter != 12*time.Second {
			t.Errorf("RetryAfter = %v, want 12s", info.RetryAfter)
		}
	})

	t.Run("Beta-RateLimit and multi-policy picks most constrained", func(t *testing.T) {
		h := http.Header{}
		h.Set("Beta-RateLimit", `"jira-quota-based";r=5000;t=120, "jira-burst-based";r=5;t=2`)
		h.Set("Beta-RateLimit-Policy", `"jira-burst-based";q=100;w=1`)

		info := ParseRateLimitHeaders(h)
		if info.Policy != "jira-burst-based" {
			t.Errorf("Policy = %q, want jira-burst-based", info.Policy)
		}
		if info.Remaining != 5 {
			t.Errorf("Remaining = %d, want 5", info.Remaining)
		}
		if info.ResetAfter != 2*time.Second {
			t.Errorf("ResetAfter = %v, want 2s", info.ResetAfter)
		}
		if info.Quota != 100 || info.Window != time.Second {
			t.Errorf("Quota=%d Window=%v, want 100 and 1s", info.Quota, info.Window)
		}
	})

	t.Run("legacy X-RateLimit-Remaining and HTTP-date Retry-After", func(t *testing.T) {
		h := http.Header{}
		h.Set("X-RateLimit-Remaining", "42")
		future := time.Now().UTC().Add(5 * time.Second).Format(http.TimeFormat)
		h.Set("Retry-After", future)

		info := ParseRateLimitHeaders(h)
		if info.Remaining != 42 {
			t.Errorf("Remaining = %d, want 42", info.Remaining)
		}
		if info.Quota != -1 {
			t.Errorf("Quota = %d, want -1", info.Quota)
		}
		if info.RetryAfter <= 0 || info.RetryAfter > 6*time.Second {
			t.Errorf("RetryAfter = %v, expected within (0, 6s]", info.RetryAfter)
		}
	})
}

func TestDoRequest_RetryPreservesPOSTAndPUTBody(t *testing.T) {
	var postBodies []string
	var putBodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.Method {
		case http.MethodPost:
			postBodies = append(postBodies, string(b))
			if len(postBodies) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			json.NewEncoder(w).Encode(map[string]int{"count": 7})
		case http.MethodPut:
			putBodies = append(putBodies, string(b))
			if len(putBodies) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	client := newTestClient(srv)

	count, err := client.SearchApproximateCount(context.Background(), "project = TEST")
	if err != nil {
		t.Fatalf("SearchApproximateCount: %v", err)
	}
	if count != 7 {
		t.Errorf("count = %d, want 7", count)
	}
	if len(postBodies) != 2 {
		t.Fatalf("expected 2 POST attempts, got %d", len(postBodies))
	}
	if postBodies[0] == "" || postBodies[0] != postBodies[1] {
		t.Errorf("POST retry body mismatch: attempt0=%q attempt1=%q", postBodies[0], postBodies[1])
	}

	if err := client.UpdateField(context.Background(), "TEST-1", "summary", "Updated title"); err != nil {
		t.Fatalf("UpdateField: %v", err)
	}
	if len(putBodies) != 2 {
		t.Fatalf("expected 2 PUT attempts, got %d", len(putBodies))
	}
	if putBodies[0] == "" || putBodies[0] != putBodies[1] {
		t.Errorf("PUT retry body mismatch: attempt0=%q attempt1=%q", putBodies[0], putBodies[1])
	}
}

func TestDoRequest_429NoDoubleSleep(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(MySelf{DisplayName: "Fast"})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	start := time.Now()
	me, err := client.MySelf(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("MySelf: %v", err)
	}
	if me.DisplayName != "Fast" {
		t.Errorf("DisplayName = %q, want Fast", me.DisplayName)
	}
	// Two 429 retries with Retry-After=0 must NOT sleep the 1s+2s exponential backoff.
	if elapsed > 500*time.Millisecond {
		t.Errorf("429 retries took %v, expected <500ms (double-sleep regression)", elapsed)
	}
}

type countingLimiter struct {
	calls atomic.Int32
}

func (l *countingLimiter) Wait(ctx context.Context) error {
	l.calls.Add(1)
	return nil
}

func TestWithRateLimiterAndObserver(t *testing.T) {
	var call atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if call.Add(1) == 1 {
			w.Header().Set("RateLimit", `"jira-quota-based";r=900;t=30`)
			w.Header().Set("X-RateLimit-NearLimit", "true")
		}
		json.NewEncoder(w).Encode(MySelf{DisplayName: "Obs"})
	}))
	defer srv.Close()

	limiter := &countingLimiter{}
	var observed []RateLimitInfo
	client := NewWithClient(
		srv.URL,
		srv.Client(),
		1,
		WithRateLimiter(limiter),
		WithRateLimitObserver(func(info RateLimitInfo) {
			observed = append(observed, info)
		}),
	)

	if _, err := client.MySelf(context.Background()); err != nil {
		t.Fatalf("first MySelf: %v", err)
	}
	if _, err := client.MySelf(context.Background()); err != nil {
		t.Fatalf("second MySelf: %v", err)
	}

	if got := limiter.calls.Load(); got != 2 {
		t.Errorf("custom limiter calls = %d, want 2", got)
	}
	if len(observed) != 2 {
		t.Fatalf("observed %d responses, want 2", len(observed))
	}
	if !observed[0].NearLimit || observed[0].Remaining != 900 || observed[0].Path != "/rest/api/3/myself" {
		t.Errorf("first observed = %+v", observed[0])
	}
	if observed[1].NearLimit || observed[1].Remaining != -1 {
		t.Errorf("second observed should clear NearLimit, got %+v", observed[1])
	}
}
