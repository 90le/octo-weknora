package sandbox

import (
	"bufio"
	"context"
	"errors"
	"io"

	"github.com/moby/moby/client"
)

// WithIMDockerOutputLimit raises the IM-only raw Docker stream ceiling for an
// explicitly bounded file read. The provider must still enforce its own
// decoded output limit. Ordinary tool and helper execs use IMMaxOutputBytes.
func WithIMDockerOutputLimit(ctx context.Context, limit int64) (context.Context, error) {
	if limit <= 0 || limit > 50<<20 {
		return nil, errors.New("Octo sandbox: invalid Docker stream limit")
	}
	return context.WithValue(ctx, imDockerOutputLimitKey{}, limit), nil
}

type imDockerOutputLimitKey struct{}

type imLimitedDockerEngine struct{ dockerEngineAPI }

const (
	imWorkspaceTmpfs = "rw,nosuid,nodev,size=100663296,mode=1777"
	imTmpTmpfs       = "rw,nosuid,nodev,size=67108864,mode=1777"
)

// IM commands do not need the generic installer capabilities. Never grant
// them back after CapDrop=ALL, even when the selected Docker config would.
func (e *imLimitedDockerEngine) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if options.HostConfig == nil {
		return client.ContainerCreateResult{}, errors.New("Octo sandbox: Docker host config unavailable")
	}
	host := *options.HostConfig
	host.CapAdd = nil
	// Do not change the host configuration shared with non-IM sessions.
	// All writable workspace data is ephemeral and subject to memory limits.
	host.ReadonlyRootfs = true
	host.Tmpfs = map[string]string{SessionWorkspaceRoot: imWorkspaceTmpfs, "/tmp": imTmpTmpfs}
	host.ShmSize = 64 << 20
	options.HostConfig = &host
	return e.dockerEngineAPI.ContainerCreate(ctx, options)
}

// An audited image ID must already be present on the selected daemon; IM
// never pulls a substitute image from a registry.
func (e *imLimitedDockerEngine) ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	return nil, errors.New("Octo sandbox: audited image is absent on the selected Docker daemon")
}

func (e *imLimitedDockerEngine) ExecAttach(ctx context.Context, id string, options client.ExecAttachOptions) (client.ExecAttachResult, error) {
	result, err := e.dockerEngineAPI.ExecAttach(ctx, id, options)
	if err != nil {
		return result, err
	}
	limit := int64(IMMaxOutputBytes)
	if explicit, ok := ctx.Value(imDockerOutputLimitKey{}).(int64); ok && explicit > 0 && explicit <= 50<<20 {
		limit = explicit
	}
	// Account for Docker's multiplex framing; exhausting this limit is an
	// error, never a successful truncated result.
	result.Reader = bufio.NewReader(&imBudgetReader{inner: result.Reader, remaining: limit + 64*1024})
	return result, nil
}

type imBudgetReader struct {
	inner     io.Reader
	remaining int64
}

func (r *imBudgetReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining <= 0 {
		var probe [1]byte
		n, err := r.inner.Read(probe[:])
		if n > 0 {
			return 0, errors.New("Octo sandbox: Docker stream exceeded limit")
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.inner.Read(p)
	r.remaining -= int64(n)
	return n, err
}
