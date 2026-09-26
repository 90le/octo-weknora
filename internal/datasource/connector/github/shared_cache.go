package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource/snapshot"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
)

const sharedGitCacheDirectory = "github-shared-git-v1"
const sharedGitCacheMarker = "weknora-cache-identity"
const staleStageCleanupLimit = 16

// SharedGitCacheEnabled is opt-in while the physical transport is rolled out.
// Disabling it leaves the established per-source transport intact.
func SharedGitCacheEnabled() bool {
	return strings.TrimSpace(os.Getenv("DATASOURCE_GITHUB_SHARED_GIT_CACHE")) == "1"
}

// SharedGitCachePresent keeps deletion cleanup active after an operator turns
// off new shared acquisitions for rollback.
func SharedGitCachePresent() bool {
	store, err := snapshot.FromEnvironment()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(store.Base, sharedGitCacheDirectory))
	return err == nil && info.IsDir()
}

// CredentialScope is a keyed equality marker, never a plain token hash. A
// public source has its own scope and cannot inherit objects from a token.
func CredentialScope(cfg *types.DataSourceConfig) (string, error) {
	t := token(cfg)
	if t == "" {
		return "public", nil
	}
	key := secutils.GetAESKey()
	if key == nil {
		return "", errors.New("GitHub private cache requires SYSTEM_AES_KEY")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("github-document-sync-credential-v1\x00"))
	_, _ = mac.Write([]byte(t))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

type sharedGitIdentity struct {
	dir      string
	lockPath string
	marker   string
}

func sharedGitIdentityFor(tenantID uint64, repository, credentialScope string) (sharedGitIdentity, error) {
	if tenantID == 0 || credentialScope == "" ||
		(credentialScope != "public" && (len(credentialScope) != 64 || !isLowerHex(credentialScope))) {
		return sharedGitIdentity{}, errors.New("GitHub shared cache identity is invalid")
	}
	repository = strings.ToLower(repository)
	if !repoPattern.MatchString(repository) {
		return sharedGitIdentity{}, errors.New("GitHub shared cache repository is invalid")
	}
	store, err := snapshot.FromEnvironment()
	if err != nil {
		return sharedGitIdentity{}, errors.New("GitHub shared cache requires private persistent storage")
	}
	key := secutils.GetAESKey()
	if key == nil {
		return sharedGitIdentity{}, errors.New("GitHub shared cache requires SYSTEM_AES_KEY")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "github-shared-git-v1\x00%d\x00github.com\x00%s\x00%s", tenantID, repository, credentialScope)
	id := hex.EncodeToString(mac.Sum(nil))
	root := filepath.Join(store.Base, sharedGitCacheDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return sharedGitIdentity{}, errors.New("GitHub shared cache cannot be prepared")
	}
	return sharedGitIdentity{dir: filepath.Join(root, id), lockPath: filepath.Join(root, id+".lock"), marker: "v1:" + id}, nil
}

func isLowerHex(value string) bool {
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			if ch < 'a' || ch > 'f' {
				return false
			}
		}
	}
	return true
}

func verifySharedGitMarker(identity sharedGitIdentity) error {
	info, err := os.Lstat(identity.dir)
	if err != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache is unavailable"}
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: "github_cache_identity", Message: "GitHub shared cache location is invalid"}
	}
	info, err = os.Lstat(filepath.Join(identity.dir, sharedGitCacheMarker))
	if err != nil || !info.Mode().IsRegular() {
		return &Error{Code: "github_cache_identity", Message: "GitHub shared cache identity does not match this source"}
	}
	data, err := os.ReadFile(filepath.Join(identity.dir, sharedGitCacheMarker))
	if err != nil || string(data) != identity.marker {
		return &Error{Code: "github_cache_identity", Message: "GitHub shared cache identity does not match this source"}
	}
	info, err = os.Lstat(filepath.Join(identity.dir, "HEAD"))
	if err != nil || !info.Mode().IsRegular() {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache is incomplete"}
	}
	return nil
}

// Every stage for this identity is created only while holding its exclusive
// lock. Once that lock is acquired again, matching UUID stages are necessarily
// abandoned by a crashed process. Limit work per attempt and reject links or
// unexpected objects rather than recursively deleting an ambiguous path.
func cleanupStaleSharedStages(identity sharedGitIdentity) error {
	root := filepath.Dir(identity.dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return errors.New("GitHub shared cache stages cannot be inspected")
	}
	prefix := filepath.Base(identity.dir) + ".stage-"
	cleaned := 0
	for _, item := range entries {
		if !strings.HasPrefix(item.Name(), prefix) {
			continue
		}
		if _, err := uuid.Parse(strings.TrimPrefix(item.Name(), prefix)); err != nil {
			return errors.New("GitHub shared cache stage identity is invalid")
		}
		path := filepath.Join(root, item.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("GitHub shared cache stage is invalid")
		}
		if cleaned == staleStageCleanupLimit {
			return errors.New("GitHub shared cache has too many abandoned stages; retry cleanup")
		}
		if err := os.RemoveAll(path); err != nil {
			return errors.New("GitHub shared cache stage cleanup failed")
		}
		cleaned++
	}
	return nil
}

// prepareSharedStage validates an entire new shallow mirror before publishing
// it. The old mirror remains available if Git, access validation, or size
// inspection fails. Caller holds the exclusive lock and removes stage on exit.
func prepareSharedStage(ctx context.Context, cache *gitCache, identity sharedGitIdentity, commit string, check func(context.Context) error) (string, error) {
	stage := identity.dir + ".stage-" + uuid.NewString()
	staged := *cache
	staged.dir = stage
	if err := staged.fetchCommit(ctx, commit); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, sharedGitCacheMarker), []byte(identity.marker), 0o600); err != nil {
		_ = os.RemoveAll(stage)
		return "", &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache identity cannot be written"}
	}
	if size, err := directorySize(stage, githubGitCacheLimit()+1); err != nil {
		_ = os.RemoveAll(stage)
		return "", &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache size cannot be inspected"}
	} else if size > githubGitCacheLimit() {
		_ = os.RemoveAll(stage)
		return "", &Error{Code: "github_cache_limit", Message: "GitHub shared cache exceeds its limit; review the repository"}
	}
	if err := check(ctx); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	return stage, nil
}

func publishSharedStage(identity sharedGitIdentity, stage string, replace bool) error {
	if replace {
		if err := os.RemoveAll(identity.dir); err != nil {
			return &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache cannot be replaced"}
		}
	}
	if err := os.Rename(stage, identity.dir); err != nil {
		return &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache cannot be published"}
	}
	return nil
}

func rebuildSharedGitCache(ctx context.Context, cache *gitCache, identity sharedGitIdentity, commit string, check func(context.Context) error) error {
	stage, err := prepareSharedStage(ctx, cache, identity, commit, check)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	return publishSharedStage(identity, stage, true)
}

func (c *Connector) sharedGitCache(ctx context.Context, cfg *types.DataSourceConfig, s selection, commit string) (*gitCache, error) {
	owner := cfg.SyncSource
	if owner == nil || owner.TenantID == 0 || owner.KnowledgeBaseID == "" || owner.DataSourceID == "" || owner.CheckAccess == nil {
		return nil, &Error{Code: "github_cache_identity", Message: "GitHub shared cache has no trusted source identity"}
	}
	scope, err := CredentialScope(cfg)
	if err != nil || scope != owner.CredentialScope {
		return nil, &Error{Code: "github_cache_identity", Message: "GitHub shared cache credential identity changed"}
	}
	identity, err := sharedGitIdentityFor(owner.TenantID, s.Repository, scope)
	if err != nil {
		return nil, &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache identity cannot be prepared"}
	}
	unlock, err := mirrorLock(ctx, identity.lockPath, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := owner.CheckAccess(ctx); err != nil {
		return nil, err
	}
	if err := cleanupStaleSharedStages(identity); err != nil {
		return nil, &Error{Code: "github_cache_unavailable", Message: err.Error()}
	}
	remote := "https://github.com/" + strings.ToLower(s.Repository) + ".git"
	allowFile := false
	if c.testGitRemote != "" {
		remote, allowFile = c.testGitRemote, true
	}
	cache := &gitCache{dir: identity.dir, remote: remote, token: token(cfg), allowFileTransport: allowFile, networkGitObserved: c.testNetworkGitObserved, strictAuth: true}
	if _, statErr := os.Lstat(identity.dir); errors.Is(statErr, os.ErrNotExist) {
		stage, err := prepareSharedStage(ctx, cache, identity, commit, owner.CheckAccess)
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stage)
		if err := publishSharedStage(identity, stage, false); err != nil {
			return nil, err
		}
	} else {
		if statErr != nil {
			return nil, &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache cannot be inspected"}
		}
		if verifyErr := verifySharedGitMarker(identity); verifyErr != nil {
			var cacheErr *Error
			if !errors.As(verifyErr, &cacheErr) || cacheErr.Code != "github_cache_unavailable" {
				return nil, verifyErr // mismatched identity is never rebuilt
			}
			if err := rebuildSharedGitCache(ctx, cache, identity, commit, owner.CheckAccess); err != nil {
				return nil, err
			}
		} else {
			if err := cache.fetchCommit(ctx, commit); err != nil {
				return nil, err
			}
			if size, sizeErr := directorySize(cache.dir, githubGitCacheLimit()+1); sizeErr != nil {
				return nil, &Error{Code: "github_cache_unavailable", Message: "GitHub shared cache size cannot be inspected"}
			} else if size > githubGitCacheLimit() {
				// Old pinned commits can accumulate across runs. Rebuild a bounded
				// shallow mirror while readers are excluded; an already planned
				// document whose object disappears fails safely and replans later.
				if err := rebuildSharedGitCache(ctx, cache, identity, commit, owner.CheckAccess); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := owner.CheckAccess(ctx); err != nil {
		return nil, err
	}
	cache.lockPath = identity.lockPath
	cache.identity = identity
	cache.checkAccess = owner.CheckAccess
	return cache, nil
}

// CleanupSharedGitCache removes physical objects only after the database-backed
// subscriber check runs under the same lock used by readers and fetchers. A
// failed check keeps bytes on disk; it must never evict another source's cache.
func CleanupSharedGitCache(ctx context.Context, tenantID uint64, repository, scope string, hasSubscribers func(context.Context) (bool, error)) error {
	identity, err := sharedGitIdentityFor(tenantID, repository, scope)
	if err != nil {
		return err
	}
	if hasSubscribers == nil {
		return errors.New("GitHub shared cache subscriber check is unavailable")
	}
	unlock, err := mirrorLock(ctx, identity.lockPath, true)
	if err != nil {
		return err
	}
	defer unlock()
	if err := cleanupStaleSharedStages(identity); err != nil {
		return err
	}
	active, err := hasSubscribers(ctx)
	if err != nil || active {
		return err
	}
	if _, err := os.Lstat(identity.dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("GitHub shared cache cannot be inspected")
	}
	if err := verifySharedGitMarker(identity); err != nil {
		return err
	}
	return os.RemoveAll(identity.dir)
}
