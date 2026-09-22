package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	releaseCatalogPerPage       = 10
	releaseCatalogMaxNoteBytes  = 64 << 10
	releaseCatalogMaxAssetCount = 20
	releaseCatalogCacheTTL      = 90 * time.Second
)

// ReleaseCatalog is a small, structured view of GitHub release metadata. It
// is deliberately separate from source snapshots: a branch's version file,
// Git tag and published GitHub Release are three different facts.
type ReleaseCatalog struct {
	Repository      string    `json:"repository"`
	CheckedAt       time.Time `json:"checked_at"`
	Releases        []Release `json:"releases"`
	Tags            []Tag     `json:"tags,omitempty"`
	HistoryComplete bool      `json:"history_complete"`
	TagsComplete    bool      `json:"tags_complete,omitempty"`
	ReleasesStale   bool      `json:"releases_stale,omitempty"`
	TagsStale       bool      `json:"tags_stale,omitempty"`
}

// LatestStable returns the latest *published, non-prerelease* GitHub Release.
// It never derives a release from a tag or a branch/package version string.
func (c ReleaseCatalog) LatestStable() (Release, bool) {
	for _, release := range c.Releases {
		if !release.Draft && !release.Prerelease && !release.PublishedAt.IsZero() {
			return release, true
		}
	}
	return Release{}, false
}

// LatestPrerelease returns the newest published prerelease. It is exposed as
// additional context only; callers must not silently substitute it for the
// latest stable release.
func (c ReleaseCatalog) LatestPrerelease() (Release, bool) {
	for _, release := range c.Releases {
		if !release.Draft && release.Prerelease && !release.PublishedAt.IsZero() {
			return release, true
		}
	}
	return Release{}, false
}

type Release struct {
	ID              int64          `json:"id"`
	TagName         string         `json:"tag_name"`
	Name            string         `json:"name,omitempty"`
	Notes           string         `json:"notes,omitempty"`
	NotesTruncated  bool           `json:"notes_truncated,omitempty"`
	URL             string         `json:"url"`
	PublishedAt     time.Time      `json:"published_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	TargetCommitish string         `json:"target_commitish,omitempty"`
	Draft           bool           `json:"draft,omitempty"`
	Prerelease      bool           `json:"prerelease,omitempty"`
	Assets          []ReleaseAsset `json:"assets,omitempty"`
}

type ReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	Size        int64  `json:"size,omitempty"`
	Digest      string `json:"digest,omitempty"`
}

// Tag is an observed Git ref. It is intentionally not represented as a
// release and has no inferred publication date or changelog.
type Tag struct {
	Name      string `json:"name"`
	CommitSHA string `json:"commit_sha"`
	URL       string `json:"url"`
}

type githubReleaseAPI struct {
	ID              int64      `json:"id"`
	TagName         string     `json:"tag_name"`
	Name            string     `json:"name"`
	Body            string     `json:"body"`
	PublishedAt     *time.Time `json:"published_at"`
	CreatedAt       time.Time  `json:"created_at"`
	TargetCommitish string     `json:"target_commitish"`
	Draft           bool       `json:"draft"`
	Prerelease      bool       `json:"prerelease"`
	Assets          []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

type githubTagAPI struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type releaseCacheEntry struct {
	Repository       string
	Releases         []Release
	ReleaseETag      string
	ReleaseCheckedAt time.Time
	ReleaseLoaded    bool
	ReleaseComplete  bool
	Tags             []Tag
	TagETag          string
	TagCheckedAt     time.Time
	TagLoaded        bool
	TagComplete      bool
}

// ReleaseCatalogCache is process-local and deliberately stores no tokens.
// ETags save GitHub rate-limit budget for repeated questions. Persistent
// history and organization-wide refresh scheduling are a later concern; this
// bounded cache must never become an authority beyond its checked_at value.
type ReleaseCatalogCache struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	entries map[string]releaseCacheEntry
}

func NewReleaseCatalogCache() *ReleaseCatalogCache {
	return &ReleaseCatalogCache{
		now:     func() time.Time { return time.Now().UTC() },
		ttl:     releaseCatalogCacheTTL,
		entries: make(map[string]releaseCacheEntry),
	}
}

func releaseCatalogCacheKey(scope, repository string, cfg *types.DataSourceConfig) string {
	// A data-source ID scopes the cache to one KB/credential relationship. The
	// credential itself is not retained; its digest only prevents a stale entry
	// surviving an in-place credential replacement.
	sum := sha256.Sum256([]byte(scope + "\x00" + strings.ToLower(repository) + "\x00" + token(cfg)))
	return hex.EncodeToString(sum[:])
}

func cloneReleases(in []Release) []Release {
	out := make([]Release, len(in))
	for i, release := range in {
		out[i] = release
		out[i].Assets = append([]ReleaseAsset(nil), release.Assets...)
	}
	return out
}

func cloneTags(in []Tag) []Tag { return append([]Tag(nil), in...) }

func (c *ReleaseCatalogCache) entry(key string) releaseCacheEntry {
	if c == nil {
		return releaseCacheEntry{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	entry.Releases = cloneReleases(entry.Releases)
	entry.Tags = cloneTags(entry.Tags)
	return entry
}

func (c *ReleaseCatalogCache) store(key string, entry releaseCacheEntry) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.Releases = cloneReleases(entry.Releases)
	entry.Tags = cloneTags(entry.Tags)
	c.entries[key] = entry
}

func (c *ReleaseCatalogCache) clock() time.Time {
	if c == nil || c.now == nil {
		return time.Now().UTC()
	}
	return c.now().UTC()
}

func (c *ReleaseCatalogCache) fresh(checkedAt time.Time, now time.Time) bool {
	if checkedAt.IsZero() {
		return false
	}
	ttl := releaseCatalogCacheTTL
	if c != nil && c.ttl >= 0 {
		ttl = c.ttl
	}
	return now.Sub(checkedAt) >= 0 && now.Sub(checkedAt) < ttl
}

// FetchReleaseCatalog retrieves published GitHub Release metadata. It uses
// the existing GitHub connector HTTP client, credential map and error
// redaction; it never reads the source Git cache, which intentionally has no
// tags. includeTags is only needed when a caller explicitly requests tags or
// when no official release exists.
func (c *Connector) FetchReleaseCatalog(
	ctx context.Context,
	cache *ReleaseCatalogCache,
	cacheScope string,
	cfg *types.DataSourceConfig,
	includeTags bool,
) (ReleaseCatalog, error) {
	if cache == nil {
		cache = NewReleaseCatalogCache()
	}
	selection, err := parseSelection(cfg)
	if err != nil {
		return ReleaseCatalog{}, err
	}
	key := releaseCatalogCacheKey(cacheScope, selection.Repository, cfg)
	now := cache.clock()
	entry := cache.entry(key)
	if entry.Repository != "" && entry.Repository != selection.Repository {
		entry = releaseCacheEntry{}
	}
	entry.Repository = selection.Repository

	needReleases := !entry.ReleaseLoaded || !cache.fresh(entry.ReleaseCheckedAt, now)
	needTags := includeTags && (!entry.TagLoaded || !cache.fresh(entry.TagCheckedAt, now))
	if needReleases {
		releases, etag, complete, notModified, fetchErr := c.fetchReleases(ctx, cfg, selection.Repository, entry.ReleaseETag)
		if fetchErr != nil {
			if !entry.ReleaseLoaded {
				return ReleaseCatalog{}, fetchErr
			}
			// Cached release metadata remains useful only when clearly marked
			// stale. Do not turn a transient API failure into a false "none".
			catalog := catalogFromEntry(entry, includeTags)
			catalog.ReleasesStale = true
			return catalog, nil
		}
		if !notModified {
			entry.Releases = releases
			entry.ReleaseETag = etag
			entry.ReleaseComplete = complete
		}
		entry.ReleaseLoaded = true
		entry.ReleaseCheckedAt = now
	}
	if needTags {
		tags, etag, complete, notModified, fetchErr := c.fetchTags(ctx, cfg, selection.Repository, entry.TagETag)
		if fetchErr != nil {
			if !entry.TagLoaded {
				return ReleaseCatalog{}, fetchErr
			}
			entry.TagCheckedAt = now
			catalog := catalogFromEntry(entry, true)
			catalog.TagsStale = true
			return catalog, nil
		}
		if !notModified {
			entry.Tags = tags
			entry.TagETag = etag
			entry.TagComplete = complete
		}
		entry.TagLoaded = true
		entry.TagCheckedAt = now
	}
	cache.store(key, entry)
	return catalogFromEntry(entry, includeTags), nil
}

func catalogFromEntry(entry releaseCacheEntry, includeTags bool) ReleaseCatalog {
	checkedAt := entry.ReleaseCheckedAt
	if includeTags && entry.TagCheckedAt.After(checkedAt) {
		checkedAt = entry.TagCheckedAt
	}
	catalog := ReleaseCatalog{
		Repository:      entry.Repository,
		CheckedAt:       checkedAt,
		Releases:        cloneReleases(entry.Releases),
		HistoryComplete: entry.ReleaseComplete,
	}
	if includeTags {
		catalog.Tags = cloneTags(entry.Tags)
		catalog.TagsComplete = entry.TagComplete
	}
	return catalog
}

func (c *Connector) fetchReleases(ctx context.Context, cfg *types.DataSourceConfig, repository, etag string) ([]Release, string, bool, bool, error) {
	var payload []githubReleaseAPI
	headers, notModified, err := c.getConditionalJSON(ctx, cfg,
		"/repos/"+repository+"/releases?per_page="+strconv.Itoa(releaseCatalogPerPage), etag, &payload, 2<<20)
	if err != nil || notModified {
		return nil, headers.Get("ETag"), false, notModified, err
	}
	releases := make([]Release, 0, len(payload))
	for _, raw := range payload {
		release, ok := normalizeRelease(repository, raw)
		if ok {
			releases = append(releases, release)
		}
	}
	sort.SliceStable(releases, func(i, j int) bool {
		if releases[i].PublishedAt.Equal(releases[j].PublishedAt) {
			return releases[i].ID > releases[j].ID
		}
		return releases[i].PublishedAt.After(releases[j].PublishedAt)
	})
	return releases, headers.Get("ETag"), !hasNextPage(headers.Get("Link")), false, nil
}

func (c *Connector) fetchTags(ctx context.Context, cfg *types.DataSourceConfig, repository, etag string) ([]Tag, string, bool, bool, error) {
	var payload []githubTagAPI
	headers, notModified, err := c.getConditionalJSON(ctx, cfg,
		"/repos/"+repository+"/tags?per_page="+strconv.Itoa(releaseCatalogPerPage), etag, &payload, 2<<20)
	if err != nil || notModified {
		return nil, headers.Get("ETag"), false, notModified, err
	}
	tags := make([]Tag, 0, len(payload))
	for _, raw := range payload {
		name := strings.TrimSpace(raw.Name)
		sha := strings.ToLower(strings.TrimSpace(raw.Commit.SHA))
		if !safeReleaseTag(name) || !shaPattern.MatchString(sha) {
			continue
		}
		tags = append(tags, Tag{Name: name, CommitSHA: sha, URL: githubTagURL(repository, name)})
	}
	return tags, headers.Get("ETag"), !hasNextPage(headers.Get("Link")), false, nil
}

func normalizeRelease(repository string, raw githubReleaseAPI) (Release, bool) {
	tag := strings.TrimSpace(raw.TagName)
	if raw.ID <= 0 || !safeReleaseTag(tag) || raw.PublishedAt == nil || raw.PublishedAt.IsZero() {
		return Release{}, false
	}
	notes, truncated := truncateReleaseNotes(raw.Body)
	release := Release{
		ID:              raw.ID,
		TagName:         tag,
		Name:            boundedReleaseText(raw.Name, 512),
		Notes:           notes,
		NotesTruncated:  truncated,
		URL:             githubReleaseURL(repository, tag),
		PublishedAt:     raw.PublishedAt.UTC(),
		CreatedAt:       raw.CreatedAt.UTC(),
		TargetCommitish: boundedReleaseText(raw.TargetCommitish, 256),
		Draft:           raw.Draft,
		Prerelease:      raw.Prerelease,
	}
	for _, asset := range raw.Assets {
		if len(release.Assets) >= releaseCatalogMaxAssetCount {
			break
		}
		name := boundedReleaseText(asset.Name, 512)
		if name == "" || !safeGitHubAssetURL(repository, asset.BrowserDownloadURL) {
			continue
		}
		release.Assets = append(release.Assets, ReleaseAsset{
			Name:        name,
			DownloadURL: asset.BrowserDownloadURL,
			Size:        asset.Size,
			Digest:      boundedReleaseText(asset.Digest, 256),
		})
	}
	return release, true
}

func safeReleaseTag(tag string) bool {
	return tag != "" && len(tag) <= 256 && utf8.ValidString(tag) && !strings.ContainsAny(tag, "\x00\r\n")
}

func boundedReleaseText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return truncateUTF8(value, max)
}

func truncateReleaseNotes(value string) (string, bool) {
	if len(value) <= releaseCatalogMaxNoteBytes {
		return value, false
	}
	return truncateUTF8(value, releaseCatalogMaxNoteBytes), true
}

func truncateUTF8(value string, max int) string {
	if len(value) <= max {
		return value
	}
	for max > 0 && !utf8.ValidString(value[:max]) {
		max--
	}
	return value[:max]
}

func githubReleaseURL(repository, tag string) string {
	return "https://github.com/" + repository + "/releases/tag/" + url.PathEscape(tag)
}

func githubTagURL(repository, tag string) string {
	return "https://github.com/" + repository + "/tree/" + url.PathEscape(tag)
}

func safeGitHubAssetURL(repository, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
		return false
	}
	return strings.HasPrefix(u.EscapedPath(), "/"+repository+"/releases/download/")
}

// getConditionalJSON follows the same credential, transport and safe-error
// policy as Connector.getWithHeaders, adding only ETag/304 support needed by
// a polling metadata catalog. It deliberately never follows redirects.
func (c *Connector) getConditionalJSON(
	ctx context.Context,
	cfg *types.DataSourceConfig,
	endpoint, etag string,
	out interface{},
	limit int64,
) (http.Header, bool, error) {
	base := apiBase
	if c != nil && c.apiBase != "" {
		base = c.apiBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+endpoint, nil)
	if err != nil {
		return nil, false, &Error{Code: "github_request", Message: "GitHub request is invalid"}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "octo-weknora")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if t := token(cfg); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, &Error{Code: "github_connection", Message: "GitHub connection failed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return resp.Header, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return resp.Header, false, githubHTTPError(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return resp.Header, false, &Error{Code: "github_response_limit", Message: "GitHub response is incomplete or exceeds the sync limit"}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.Header, false, &Error{Code: "github_response_invalid", Message: "GitHub response is invalid"}
	}
	return resp.Header, false, nil
}
