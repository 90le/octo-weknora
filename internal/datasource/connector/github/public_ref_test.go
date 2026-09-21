package github

import (
	"context"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestPublicRefResolutionPinsHeadBranchesAndPeeledTags(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	raw := "ref: refs/heads/main\tHEAD\n" + a + "\tHEAD\n" + a + "\trefs/heads/main\n" + b + "\trefs/tags/v1\n" + a + "\trefs/tags/v1^{}\n"
	for _, ref := range []string{"", "main", "v1", "refs/heads/main", "refs/tags/v1"} {
		sha, err := selectPublicRef(raw, ref)
		require.NoError(t, err)
		require.Equal(t, a, sha)
	}
	_, err := selectPublicRef(raw+b+"\trefs/tags/main\n", "main")
	require.Error(t, err)
	_, err = selectPublicRef(raw, "missing")
	require.Error(t, err)
}

func TestPublicRefCommandDoesNotRequireTMPDIRWorkingDirectory(t *testing.T) {
	cmd := publicRefCommand(context.Background(), "ls-remote", "https://github.com/example/repository.git")
	require.Empty(t, cmd.Dir)
}
