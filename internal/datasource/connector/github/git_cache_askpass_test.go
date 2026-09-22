package github

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitCacheTokenedInitialCloneCreatesAskPassInPrivateParent(t *testing.T) {
	cacheParent := filepath.Join(t.TempDir(), "private", "git")
	cacheDir := filepath.Join(cacheParent, "repository")
	cache := &gitCache{
		dir:    cacheDir,
		remote: "https://github.com/example/private-repository.git",
		token:  "test-token",
	}

	cmd, cleanup, err := cache.commandIn(context.Background(), "", "clone", "--bare", "--depth=1", "--no-tags", "--", cache.remote, cache.dir)
	require.NoError(t, err)

	var askPass string
	for _, value := range cmd.Env {
		if strings.HasPrefix(value, "GIT_ASKPASS=") {
			askPass = strings.TrimPrefix(value, "GIT_ASKPASS=")
			break
		}
	}
	require.NotEmpty(t, askPass)
	require.Equal(t, cacheParent, filepath.Dir(askPass))
	require.FileExists(t, askPass)
	// git clone must create the cache directory itself. The helper therefore
	// cannot live in cacheDir during the first token-authenticated clone.
	_, statErr := os.Stat(cacheDir)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	cleanup()
	_, statErr = os.Stat(askPass)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestGitCacheAskPassPreparationDoesNotExposePrivatePath(t *testing.T) {
	blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blockedParent, []byte("not a directory"), 0o600))
	cache := &gitCache{dir: filepath.Join(blockedParent, "repository"), token: "test-token"}

	_, cleanup, err := cache.commandIn(context.Background(), "", "clone", "--bare", "--", "https://github.com/example/private-repository.git", cache.dir)
	cleanup()
	require.Error(t, err)
	var githubErr *Error
	require.True(t, errors.As(err, &githubErr))
	require.Equal(t, "github_cache_unavailable", githubErr.Code)
	require.Equal(t, "GitHub source cache cannot prepare credentials", githubErr.Message)
	require.NotContains(t, err.Error(), blockedParent)
}
