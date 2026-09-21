package github

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

const discoverPageSize = 100
const maxDiscoverPages = 5

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// Repository is safe repository metadata used by the batch-import picker. It
// has no clone URL, credential, issue, or contributor data.
type Repository = types.GitHubRepositoryCandidate

type RepositoryPage struct {
	Owner        string       `json:"owner"`
	Repositories []Repository `json:"repositories"`
	NextCursor   string       `json:"next_cursor,omitempty"`
}

type githubRepository struct {
	FullName      string    `json:"full_name"`
	DefaultBranch string    `json:"default_branch"`
	Description   string    `json:"description"`
	Archived      bool      `json:"archived"`
	Disabled      bool      `json:"disabled"`
	Fork          bool      `json:"fork"`
	Size          int       `json:"size"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DiscoverRepositories lists one page of repositories owned by an
// organization or user. It deliberately does not create any source or begin a
// sync; callers must explicitly select repositories in a later request.
func (c *Connector) DiscoverRepositories(ctx context.Context, cfg *types.DataSourceConfig, owner, cursor string) (*RepositoryPage, error) {
	owner = strings.TrimSpace(owner)
	if !ValidOwner(owner) {
		return nil, &Error{Code: "github_owner_invalid", Message: "GitHub organization or user name is invalid"}
	}
	page := 1
	if cursor != "" {
		var err error
		page, err = strconv.Atoi(cursor)
		if err != nil || page < 1 || page > maxDiscoverPages {
			return nil, &Error{Code: "github_cursor_invalid", Message: "GitHub repository page cursor is invalid"}
		}
	}
	query := url.Values{"type": {"sources"}, "sort": {"pushed"}, "direction": {"desc"}, "per_page": {strconv.Itoa(discoverPageSize)}, "page": {strconv.Itoa(page)}}.Encode()
	var entries []githubRepository
	headers, err := c.getWithHeaders(ctx, cfg, "/orgs/"+url.PathEscape(owner)+"/repos?"+query, &entries, 4<<20)
	if err != nil {
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "github_not_found" {
			return nil, err
		}
		entries = nil
		headers, err = c.getWithHeaders(ctx, cfg, "/users/"+url.PathEscape(owner)+"/repos?"+query, &entries, 4<<20)
		if err != nil {
			return nil, err
		}
	}
	result := &RepositoryPage{Owner: owner, Repositories: make([]Repository, 0, len(entries))}
	for _, entry := range entries {
		if !repoPattern.MatchString(entry.FullName) || !strings.EqualFold(strings.Split(entry.FullName, "/")[0], owner) {
			return nil, &Error{Code: "github_repository_invalid", Message: "GitHub returned an unexpected repository identity"}
		}
		result.Repositories = append(result.Repositories, Repository{
			Repository: entry.FullName, DefaultBranch: entry.DefaultBranch, Description: entry.Description,
			Archived: entry.Archived, Disabled: entry.Disabled, Fork: entry.Fork, SizeKiB: entry.Size, UpdatedAt: entry.UpdatedAt,
		})
	}
	if page < maxDiscoverPages && hasNextPage(headers.Get("Link")) {
		result.NextCursor = strconv.Itoa(page + 1)
	}
	return result, nil
}

func ValidOwner(owner string) bool {
	return ownerPattern.MatchString(strings.TrimSpace(owner))
}

func hasNextPage(link string) bool {
	for _, value := range strings.Split(link, ",") {
		if strings.Contains(value, `rel="next"`) || strings.Contains(value, "rel=next") {
			return true
		}
	}
	return false
}
