package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type APIError struct {
	Code       string        `json:"code"`
	Message    string        `json:"message"`
	Status     int           `json:"status,omitempty"`
	RequestID  string        `json:"request_id,omitempty"`
	RetryAfter time.Duration `json:"-"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

type client struct {
	server, token, csrf, organization string
	secrets                           []string
	http                              *http.Client
}

type response struct {
	Body   json.RawMessage
	Header http.Header
	Status int
}

func newClient(server, token string) *client {
	return &client{server: server, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) enableSession() {
	jar, _ := cookiejar.New(nil)
	c.http.Jar = jar
}

func (c *client) request(ctx context.Context, method, path string, body []byte, headers http.Header) (response, error) {
	if !strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n#") {
		return response{}, errors.New("invalid API path")
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || strings.Contains(parsed.Path, "..") {
		return response{}, errors.New("invalid API path")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, bytes.NewReader(body))
	if err != nil {
		return response{}, errors.New("cannot construct request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "runnerctl")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if c.organization != "" {
		req.Header.Set("X-Organization-ID", c.organization)
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, &APIError{Code: "transport_error", Message: "request failed; check connectivity, TLS and server address"}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 {
		return response{}, &APIError{Code: "invalid_response", Message: "response unreadable or exceeds 4 MiB", Status: res.StatusCode}
	}
	result := response{Body: data, Header: res.Header, Status: res.StatusCode}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		e := &APIError{Code: "http_error", Message: http.StatusText(res.StatusCode), Status: res.StatusCode, RequestID: res.Header.Get("X-Request-ID")}
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Code != "" {
			e.Code = safeText(envelope.Error.Code, 128)
			e.Message = safeText(envelope.Error.Message, 1024)
		}
		// Servers must not echo credentials; redact known secrets even if a proxy does.
		for _, secret := range append([]string{c.token, c.csrf}, c.secrets...) {
			if secret != "" {
				e.Message = strings.ReplaceAll(e.Message, secret, "[redacted]")
				e.Code = strings.ReplaceAll(e.Code, secret, "[redacted]")
				e.RequestID = strings.ReplaceAll(e.RequestID, secret, "[redacted]")
			}
		}
		e.RequestID = safeText(e.RequestID, 128)
		e.RetryAfter = retryAfter(res.Header.Get("Retry-After"), time.Now())
		return result, e
	}
	if len(data) > 0 && !json.Valid(data) {
		return result, &APIError{Code: "invalid_response", Message: "expected JSON response", Status: res.StatusCode}
	}
	return result, nil
}

func safeText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		if seconds > 3600 {
			seconds = 3600
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		d := date.Sub(now)
		if d > time.Hour {
			return time.Hour
		}
		if d > 0 {
			return d
		}
	}
	return 0
}

func decodeResponse[T any](res response) (T, error) {
	var value T
	if len(res.Body) == 0 {
		return value, nil
	}
	if err := json.Unmarshal(res.Body, &value); err != nil {
		return value, fmt.Errorf("invalid server response")
	}
	return value, nil
}

func (c *client) json(ctx context.Context, method, path string, input any) (response, error) {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return response{}, err
		}
	}
	return c.request(ctx, method, path, body, nil)
}
