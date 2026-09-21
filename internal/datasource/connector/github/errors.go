package github

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Error is a stable, safe-to-display explanation for a GitHub operation. It
// deliberately contains no request URL, headers, or credential value.
type Error struct {
	Code       string
	Message    string
	RetryAfter *time.Time
}

func (e *Error) Error() string {
	if e == nil {
		return "GitHub operation failed"
	}
	if e.RetryAfter != nil {
		return fmt.Sprintf("%s; retry after %s", e.Message, e.RetryAfter.UTC().Format(time.RFC3339))
	}
	return e.Message
}

var errGitUnavailable = errors.New("git executable is unavailable")

func githubHTTPError(resp *http.Response) error {
	if resp == nil {
		return &Error{Code: "github_unavailable", Message: "GitHub did not return a response"}
	}
	status := resp.StatusCode
	switch status {
	case http.StatusUnauthorized:
		return &Error{Code: "github_auth", Message: "GitHub authentication failed; verify the read-only token"}
	case http.StatusNotFound:
		return &Error{Code: "github_not_found", Message: "GitHub repository was not found or this token cannot read it"}
	case http.StatusForbidden, http.StatusTooManyRequests:
		if remaining := strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")); remaining == "0" {
			return &Error{Code: "github_rate_limit", Message: "GitHub API rate limit is exhausted", RetryAfter: githubReset(resp.Header)}
		}
		if retry := retryAfter(resp.Header); retry != nil {
			return &Error{Code: "github_secondary_rate_limit", Message: "GitHub temporarily limited this sync", RetryAfter: retry}
		}
		return &Error{Code: "github_forbidden", Message: "GitHub rejected this request; check repository access and rate limits"}
	default:
		return &Error{Code: "github_http", Message: fmt.Sprintf("GitHub API returned HTTP %d", status)}
	}
}

func githubReset(h http.Header) *time.Time {
	v, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64)
	if err != nil || v <= 0 {
		return retryAfter(h)
	}
	t := time.Unix(v, 0).UTC()
	return &t
}

func retryAfter(h http.Header) *time.Time {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if seconds, err := strconv.Atoi(v); err == nil && seconds > 0 {
		t := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		return &t
	}
	if when, err := http.ParseTime(v); err == nil {
		when = when.UTC()
		return &when
	}
	return nil
}
