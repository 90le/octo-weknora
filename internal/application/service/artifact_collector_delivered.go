package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

// PersistDeliveredText stores one already selected, confirmed delivery. The
// caller must supply a live check of the original turn's delivery authority;
// a tenant, session, or filename alone is never an authorization. No sandbox is
// inspected. If message persistence later fails, call DiscardDeliveredText.
func (c *ArtifactCollector) PersistDeliveredText(
	ctx context.Context, sessionID, messageID string, tenantID uint64,
	name string, data []byte, authorize func(context.Context) error,
) (types.MessageArtifact, error) {
	if c == nil || c.fileService == nil || authorize == nil || tenantID == 0 ||
		strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" ||
		name == "" || name != strings.TrimSpace(name) || name != path.Base(name) ||
		name == "." || strings.ContainsAny(name, "\\\r\n\x00") || len(name) > 255 ||
		path.Ext(name) == "" || !utf8.ValidString(name) || !utf8.Valid(data) ||
		len(data) == 0 || len(data) > 1<<20 || strings.ContainsRune(string(data), 0) ||
		strings.TrimSpace(string(data)) == "" {
		return types.MessageArtifact{}, errors.New("invalid delivered text artifact")
	}
	actualTenant, ok := types.SessionTenantIDFromContext(ctx)
	actualSession, sessionOK := types.SessionIDFromContext(ctx)
	if !ok || actualTenant != tenantID || !sessionOK || actualSession != sessionID {
		return types.MessageArtifact{}, errors.New("delivered artifact scope mismatch")
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return authorize(ctx)
	}
	if err := check(); err != nil {
		return types.MessageArtifact{}, err
	}
	// Use the common storage and message-resource binding path. A unique object
	// is created for each delivery; no other files are enumerated or read.
	artifact, stored := c.persistBytes(ctx, sessionID, messageID, tenantID,
		sandbox.RemoteDirEntry{Name: name}, data, artifactContentHash(data), nil)
	if !stored {
		return types.MessageArtifact{}, errors.New("failed to store delivered artifact")
	}
	if err := check(); err != nil {
		if cleanupErr := c.DiscardDeliveredText(ctx, messageID, artifact); cleanupErr != nil {
			return types.MessageArtifact{}, errors.Join(err, fmt.Errorf("artifact cleanup: %w", cleanupErr))
		}
		return types.MessageArtifact{}, err
	}
	return artifact, nil
}

// DiscardDeliveredText releases an uncommitted artifact after a failed message
// write. It must only be used for a result of PersistDeliveredText, before that
// artifact is attached to a message. Cleanup continues even if the turn ended.
func (c *ArtifactCollector) DiscardDeliveredText(
	ctx context.Context, messageID string, artifact types.MessageArtifact,
) error {
	if c == nil || c.fileService == nil || messageID == "" || artifact.URL == "" {
		return errors.New("invalid delivered artifact cleanup")
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if _, isResource := types.ParseResourcePath(artifact.URL); isResource && c.catalog != nil {
		remaining, err := c.catalog.Release(cleanupCtx, artifact.URL, artifactBindingOwnerType, messageID)
		if err != nil {
			return err
		}
		if remaining != 0 {
			return fmt.Errorf("artifact cleanup has %d remaining or unknown bindings", remaining)
		}
	}
	return c.fileService.DeleteFile(cleanupCtx, artifact.URL)
}
