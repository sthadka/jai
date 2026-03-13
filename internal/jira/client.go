package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Client is a Jira Cloud HTTP client.
type Client struct {
	baseURL    string
	email      string
	token      string
	httpClient *http.Client
	limiter    *rate.Limiter
}

// New creates a new Jira client.
func New(baseURL, email, token string, ratePerSec float64) *Client {
	if ratePerSec <= 0 {
		ratePerSec = 10
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		email:   email,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		limiter: rate.NewLimiter(rate.Limit(ratePerSec), int(ratePerSec)),
	}
}

// doRequest performs an authenticated request with rate limiting and retry.
func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.baseURL + path

	var lastErr error
	for attempt := 0; attempt <= 3; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter: 1s, 2s, 4s ± 20%.
			base := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			jitter := time.Duration(rand.Float64()*0.4*float64(base) - 0.2*float64(base))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(base + jitter):
			}
		}

		// Wait for rate limiter.
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(c.email, c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// Retry on 429 (respect Retry-After) and 5xx.
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			retryAfter := resp.Header.Get("Retry-After")
			wait := 2 * time.Second
			if retryAfter != "" {
				if secs, err := parseRetryAfter(retryAfter); err == nil {
					wait = secs
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			lastErr = fmt.Errorf("rate limited (429)")
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("server error: %d", resp.StatusCode)
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

func parseRetryAfter(s string) (time.Duration, error) {
	var secs float64
	_, err := fmt.Sscanf(s, "%f", &secs)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// get performs a GET request and decodes the JSON response.
func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jira API error %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// MySelf calls /rest/api/3/myself to test the connection.
func (c *Client) MySelf(ctx context.Context) (*MySelf, error) {
	var m MySelf
	if err := c.get(ctx, "/rest/api/3/myself", &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Fields fetches all field definitions.
func (c *Client) Fields(ctx context.Context) ([]*Field, error) {
	var fields []*Field
	if err := c.get(ctx, "/rest/api/3/field", &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// SearchAll returns a paginated iterator over issues matching the JQL query.
// It yields each page of issues. The caller iterates with a for range loop.
func (c *Client) SearchAll(ctx context.Context, jql string, fields []string) func(yield func([]*Issue, error) bool) {
	return func(yield func([]*Issue, error) bool) {
		startAt := 0
		pageSize := 100
		fieldsParam := strings.Join(fields, ",")

		for {
			path := fmt.Sprintf(
				"/rest/api/3/search?jql=%s&startAt=%d&maxResults=%d&fields=%s",
				jqlEncode(jql), startAt, pageSize, fieldsParam,
			)

			var resp SearchResponse
			if err := c.get(ctx, path, &resp); err != nil {
				yield(nil, err)
				return
			}

			if len(resp.Issues) == 0 {
				return
			}

			if !yield(resp.Issues, nil) {
				return
			}

			startAt += len(resp.Issues)
			if startAt >= resp.Total {
				return
			}
		}
	}
}

// SearchCount returns the total number of issues matching a JQL query.
func (c *Client) SearchCount(ctx context.Context, jql string) (int, error) {
	path := fmt.Sprintf("/rest/api/3/search?jql=%s&maxResults=0", jqlEncode(jql))
	var resp SearchResponse
	if err := c.get(ctx, path, &resp); err != nil {
		return 0, err
	}
	return resp.Total, nil
}

func jqlEncode(jql string) string {
	return strings.NewReplacer(
		" ", "%20",
		"=", "%3D",
		"\"", "%22",
		"'", "%27",
		">", "%3E",
		"<", "%3C",
		"(", "%28",
		")", "%29",
	).Replace(jql)
}
