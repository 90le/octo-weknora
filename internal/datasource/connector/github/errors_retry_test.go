package github

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGitHubHTTPErrorSeparatesRateLimitsFromPermanentFailures(t *testing.T) {
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		name, code         string
		status             int
		headers            http.Header
		retryable, hasHint bool
	}{
		{"primary 403 reset", "github_rate_limit", http.StatusForbidden,
			http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {""}}, true, true},
		{"secondary 429 retry after", "github_secondary_rate_limit", http.StatusTooManyRequests,
			http.Header{"Retry-After": {"60"}}, true, true},
		{"unauthorized", "github_auth", http.StatusUnauthorized, nil, false, false},
		{"not found", "github_not_found", http.StatusNotFound, nil, false, false},
		{"forbidden without rate evidence", "github_forbidden", http.StatusForbidden, nil, false, false},
		{"server failure", "github_http", http.StatusServiceUnavailable, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "primary 403 reset" {
				tc.headers.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			}
			err := githubHTTPError(&http.Response{StatusCode: tc.status, Header: tc.headers})
			gitErr, ok := err.(*Error)
			require.True(t, ok)
			require.Equal(t, tc.code, gitErr.Code)
			require.Equal(t, tc.retryable, gitErr.Retryable())
			at, hasHint := gitErr.RetryAfterAt()
			require.Equal(t, tc.hasHint, hasHint)
			if tc.name == "primary 403 reset" {
				require.True(t, at.Equal(reset))
			}
		})
	}
	for _, code := range []string{
		"github_selected_path_missing", "github_tree_truncated", "github_documents_limit",
		"github_document_file_limit", "github_documents_batch_limit", "github_response_limit",
		"github_archive_limit", "github_git_tree_limit", "github_ref_invalid",
	} {
		require.False(t, (&Error{Code: code}).Retryable(), code)
	}
}
