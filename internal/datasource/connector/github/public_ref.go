package github

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type boundedGitOutput struct{ bytes.Buffer }

func (b *boundedGitOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("repository ref list exceeds limit")
	}
	return b.Buffer.Write(p)
}

// Public source discovery uses Git's read-only ref advertisement, avoiding the
// shared anonymous REST quota. No checkout, credential helper or repo config is
// used. Private repositories stay on the authenticated API/archive path.
func (c *Connector) publicHead(ctx context.Context, s selection) (string, error) {
	if shaPattern.MatchString(s.Ref) {
		if c.publicCommitExists(ctx, s.Repository, s.Ref) {
			return s.Ref, nil
		}
		return "", errors.New("public repository commit unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	patterns := []string{"HEAD"}
	if s.Ref != "" {
		if strings.HasPrefix(s.Ref, "refs/") {
			patterns = []string{s.Ref, s.Ref + "^{}"}
		} else {
			patterns = []string{"refs/heads/" + s.Ref, "refs/tags/" + s.Ref, "refs/tags/" + s.Ref + "^{}"}
		}
	}
	args := append([]string{"-c", "credential.helper=", "-c", "core.askPass=", "ls-remote", "--symref", "--", "https://github.com/" + s.Repository + ".git"}, patterns...)
	cmd := publicRefCommand(ctx, args...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https")
	var out boundedGitOutput
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return "", errors.New("public GitHub refs unavailable; check network or repository access")
	}
	return selectPublicRef(out.String(), s.Ref)
}

// publicRefCommand deliberately has no working directory: ls-remote is a
// network-only read and must still work before an optional TMPDIR exists.
func publicRefCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "git", args...)
}
func selectPublicRef(raw, ref string) (string, error) {
	refs := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) == 2 && shaPattern.MatchString(parts[0]) {
			refs[parts[1]] = parts[0]
		}
	}
	if ref == "" {
		if sha := refs["HEAD"]; sha != "" {
			return sha, nil
		}
	}
	if strings.HasPrefix(ref, "refs/") {
		if sha := refs[ref+"^{}"]; sha != "" {
			return sha, nil
		}
		if sha := refs[ref]; sha != "" {
			return sha, nil
		}
	}
	branch, tag := refs["refs/heads/"+ref], refs["refs/tags/"+ref]
	if peeled := refs["refs/tags/"+ref+"^{}"]; peeled != "" {
		tag = peeled
	}
	if branch != "" && tag != "" && branch != tag {
		return "", errors.New("ambiguous repository ref; use refs/heads/ or refs/tags/")
	}
	if branch != "" {
		return branch, nil
	}
	if tag != "" {
		return tag, nil
	}
	return "", errors.New("public repository ref not found")
}
func (c *Connector) publicCommitExists(ctx context.Context, repo, commit string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://codeload.github.com/"+repo+"/tar.gz/"+commit, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "octo-weknora")
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
