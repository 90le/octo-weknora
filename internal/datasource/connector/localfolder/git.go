package localfolder

import (
	"bytes"
	"context"
	"crypto/sha1" // Git blob IDs use SHA-1; this is content matching, not authentication.
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type gitInfo struct {
	Commit, Remote string
	Files          map[string]string
}
type limitedBuffer struct {
	bytes.Buffer
	Max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.Max {
		return 0, fmt.Errorf("git metadata exceeds limit")
	}
	return b.Buffer.Write(p)
}
func inspectGit(ctx context.Context, root string) gitInfo {
	g := gitInfo{Files: map[string]string{}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	run := func(args ...string) ([]byte, error) {
		// These are read-only builtins. Never invoke filters, textconv, hooks,
		// status, fetch or any repository-defined command.
		base := []string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "safe.directory=" + root, "-C", root}
		cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "GIT_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
		var out limitedBuffer
		out.Max = 16 << 20
		cmd.Stdout = &out
		err := cmd.Run()
		return out.Bytes(), err
	}
	sha, err := run("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return g
	}
	g.Commit = strings.TrimSpace(string(sha))
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(g.Commit) {
		g.Commit = ""
		return g
	}
	remote, err := run("config", "--local", "--no-includes", "--get", "remote.origin.url")
	if err != nil {
		return g
	}
	r := strings.TrimSpace(string(remote))
	if strings.HasPrefix(r, "git@github.com:") {
		r = "https://github.com/" + strings.TrimPrefix(r, "git@github.com:")
	}
	u, err := url.Parse(r)
	if err != nil || u.Hostname() != "github.com" || u.RawQuery != "" {
		return g
	}
	repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repo) {
		return g
	}
	g.Remote = "https://github.com/" + repo
	tree, err := run("ls-tree", "-rz", "--full-tree", g.Commit)
	if err != nil {
		g.Remote = ""
		return g
	}
	for _, line := range bytes.Split(tree, []byte{0}) {
		parts := bytes.SplitN(line, []byte{'\t'}, 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(string(parts[0]))
		if len(fields) == 3 && fields[1] == "blob" {
			g.Files[string(parts[1])] = fields[2]
		}
	}
	return g
}
func (g gitInfo) reference(p string, body []byte) (string, string) {
	if g.Remote == "" || g.Files[p] == "" {
		return "", ""
	}
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(body))
	_, _ = h.Write(body)
	if hex.EncodeToString(h.Sum(nil)) != g.Files[p] {
		return "", ""
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return g.Remote + "/blob/" + g.Commit + "/" + strings.Join(parts, "/"), g.Commit
}
