package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"unicode"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"golang.org/x/image/tiff"
	"gorm.io/gorm"
)

const (
	// maxKBImageReadBytes caps one knowledge-base image fetched for VLM/sandbox staging.
	maxKBImageReadBytes int64 = 10 << 20
	// maxKBSourceStageBytes caps one knowledge-base source file staged into /workspace/input/kb/.
	// Heavy archives (e.g. 40-160 MiB .pptx decks) are intentionally excluded so they do not
	// exhaust bounded IM tmpfs; agents read their text via read_document and images via resource://.
	maxKBSourceStageBytes int64 = 15 << 20
	// maxSessionKBStagedBytes caps cumulative KB bytes staged into one turn's sandbox input dir.
	maxSessionKBStagedBytes int64 = 40 << 20

	kbSandboxInputSubdir = "kb"
)

type kbResourceOwner struct {
	KnowledgeID string
	Relation    string
}

type kbResourceLookup interface {
	GetActiveResourceByHandle(ctx context.Context, handle string) (*types.StoredResource, error)
	ListKnowledgeOwners(ctx context.Context, tenantID uint64, resourceID, resourceRef string) ([]kbResourceOwner, error)
}

type gormKBResourceLookup struct {
	db *gorm.DB
}

func (l *gormKBResourceLookup) GetActiveResourceByHandle(
	ctx context.Context, handle string,
) (*types.StoredResource, error) {
	if l == nil || l.db == nil || strings.TrimSpace(handle) == "" {
		return nil, nil
	}
	var res types.StoredResource
	err := l.db.WithContext(ctx).
		Where("handle = ? AND deleted_at IS NULL AND state = ?", handle, types.ResourceStateActive).
		First(&res).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (l *gormKBResourceLookup) ListKnowledgeOwners(
	ctx context.Context, tenantID uint64, resourceID, resourceRef string,
) ([]kbResourceOwner, error) {
	if l == nil || l.db == nil || tenantID == 0 {
		return nil, nil
	}
	var rows []struct {
		OwnerID  string
		Relation string
	}
	if resourceID != "" {
		if err := l.db.WithContext(ctx).
			Table("resource_bindings").
			Select("owner_id, relation").
			Where("resource_id = ? AND tenant_id = ? AND owner_type = ?",
				resourceID, tenantID, types.ResourceOwnerKnowledge).
			Find(&rows).Error; err != nil {
			return nil, err
		}
	}
	owners := make([]kbResourceOwner, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		id := strings.TrimSpace(r.OwnerID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		owners = append(owners, kbResourceOwner{
			KnowledgeID: id,
			Relation:    strings.TrimSpace(r.Relation),
		})
	}
	if len(owners) > 0 || strings.TrimSpace(resourceRef) == "" {
		return owners, nil
	}

	// Fallback for legacy knowledge rows whose file_path points to resource://<handle>
	// without an explicit resource_bindings row.
	var kIDs []string
	if err := l.db.WithContext(ctx).
		Table("knowledges").
		Where("tenant_id = ? AND file_path = ? AND deleted_at IS NULL", tenantID, resourceRef).
		Pluck("id", &kIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range kIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		owners = append(owners, kbResourceOwner{
			KnowledgeID: id,
			Relation:    types.ResourceRelationSourceFile,
		})
	}
	return owners, nil
}

type kbResourceReader struct {
	lookup           kbResourceLookup
	knowledgeService interfaces.KnowledgeService
	fileService      interfaces.FileService
	searchTargets    types.SearchTargets
	sessionID        string
	sandboxStore     sandbox.SessionFileStore
	inputDir         string

	mu               sync.Mutex
	staged           map[string]string
	totalStagedBytes int64
}

func (r *kbResourceReader) allowedResourceTenant(tenantID uint64) bool {
	if tenantID == 0 {
		return false
	}
	for _, target := range r.searchTargets {
		if target == nil {
			continue
		}
		if target.TenantID == tenantID {
			return true
		}
	}
	return false
}

func (r *kbResourceReader) ReadKnowledgeResource(
	ctx context.Context, ref string, stage bool,
) (*tools.KnowledgeResourceReadResult, error) {
	if r == nil || r.lookup == nil || r.knowledgeService == nil || r.fileService == nil {
		return nil, errors.New("knowledge base resource reader is unavailable")
	}
	handle, ok := types.ParseResourcePath(ref)
	if !ok {
		return nil, fmt.Errorf("invalid knowledge resource path %q; expected resource://<handle>", ref)
	}
	canonicalRef := types.BuildResourcePath(handle)

	res, err := r.lookup.GetActiveResourceByHandle(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("lookup knowledge resource: %w", err)
	}
	if res == nil {
		return nil, errors.New("resource is not within the current authorized knowledge scope")
	}
	if len(r.searchTargets) == 0 {
		return nil, errors.New("resource is not within the current authorized knowledge scope")
	}

	// If SearchTargets record explicit tenant IDs, verify the resource belongs to an in-scope tenant
	// (or the caller's tenant when target.TenantID is 0).
	callerTenant, _ := types.TenantIDFromContext(ctx)
	if !r.allowedResourceTenant(res.TenantID) && (callerTenant == 0 || res.TenantID != callerTenant) {
		return nil, errors.New("resource is not within the current authorized knowledge scope")
	}

	owners, err := r.lookup.ListKnowledgeOwners(ctx, res.TenantID, res.ID, canonicalRef)
	if err != nil {
		return nil, fmt.Errorf("resolve knowledge resource ownership: %w", err)
	}
	if len(owners) == 0 {
		return nil, errors.New("resource is not within the current authorized knowledge scope")
	}

	// Authorize against the exact SearchTargets scope (preserving IM published-only & @mention rules).
	ownerCtx := context.WithValue(ctx, types.TenantIDContextKey, res.TenantID)
	var authorizedKnowledge *types.Knowledge
	var authorizedRelation string
	for _, owner := range owners {
		k, kErr := r.knowledgeService.GetKnowledgeByIDOnly(ownerCtx, owner.KnowledgeID)
		if kErr != nil || k == nil || k.TenantID != res.TenantID {
			continue
		}
		if _, authErr := tools.AuthorizeLoadedKnowledge(ownerCtx, r.searchTargets, k, r.knowledgeService); authErr == nil {
			authorizedKnowledge = k
			authorizedRelation = owner.Relation
			break
		}
	}
	if authorizedKnowledge == nil {
		return nil, errors.New("resource is not within the current authorized knowledge scope")
	}

	mimeType := strings.ToLower(strings.TrimSpace(res.MimeType))
	isImage := strings.EqualFold(res.Kind, "image") ||
		authorizedRelation == types.ResourceRelationExtractedImage ||
		strings.HasPrefix(mimeType, "image/")

	limitBytes := maxKBSourceStageBytes
	if isImage {
		limitBytes = maxKBImageReadBytes
	}

	origName := strings.TrimSpace(res.OriginalName)
	if origName == "" && authorizedRelation == types.ResourceRelationSourceFile {
		origName = strings.TrimSpace(authorizedKnowledge.FileName)
	}

	out := &tools.KnowledgeResourceReadResult{
		Handle:        handle,
		ResourceRef:   canonicalRef,
		Kind:          res.Kind,
		Relation:      authorizedRelation,
		MimeType:      mimeType,
		OriginalName:  origName,
		DocumentTitle: strings.TrimSpace(authorizedKnowledge.Title),
		KnowledgeID:   authorizedKnowledge.ID,
		Size:          res.Size,
		LimitBytes:    limitBytes,
	}

	if res.Size > limitBytes {
		out.TooLarge = true
		return out, nil
	}

	reader, err := r.fileService.GetFile(ownerCtx, res.PhysicalPath)
	if err != nil {
		return nil, fmt.Errorf("read knowledge resource bytes: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, limitBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read knowledge resource bytes: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close knowledge resource stream: %w", closeErr)
	}
	if int64(len(data)) > limitBytes {
		out.Size = int64(len(data))
		out.TooLarge = true
		return out, nil
	}

	if mimeType == "" && len(data) > 0 {
		mimeType = strings.ToLower(http.DetectContentType(data))
	}
	// Convert TIFF images to PNG so both VLM and headless Chromium/PowerPoint consume them natively.
	if isTIFFImage(mimeType, origName) {
		if converted, convErr := transcodeTIFFToPNG(data); convErr == nil {
			data = converted
			mimeType = "image/png"
			if origName != "" {
				origName = strings.TrimSuffix(origName, path.Ext(origName)) + ".png"
			}
		} else {
			logger.Warnf(ctx, "[KBResource] TIFF to PNG conversion failed for %s: %v", canonicalRef, convErr)
		}
	}

	out.Data = data
	out.Size = int64(len(data))
	out.MimeType = mimeType
	out.OriginalName = origName

	if stage && r.sandboxStore != nil && strings.TrimSpace(r.sessionID) != "" && strings.TrimSpace(r.inputDir) != "" {
		stagedPath, stageErr := r.stageIntoSandbox(ctx, handle, origName, mimeType, isImage, data)
		if stageErr != nil {
			out.StageError = stageErr.Error()
		} else {
			out.StagedPath = stagedPath
		}
	}
	return out, nil
}

func (r *kbResourceReader) stageIntoSandbox(
	ctx context.Context,
	handle, origName, mimeType string,
	isImage bool,
	data []byte,
) (string, error) {
	// For Octo IM turns, require explicit shell or skill sandbox authorization and live revalidation.
	if types.HasIMKnowledgeScope(ctx) {
		grant, ok := types.IMSandboxGrantFromContext(ctx)
		if !ok || (!grant.AllowShell && !grant.AllowSkills) {
			return "", errors.New("sandbox staging is not authorized for this channel")
		}
		if grant.Revalidate != nil {
			if err := grant.Revalidate(ctx); err != nil {
				return "", fmt.Errorf("sandbox authorization revalidation failed: %w", err)
			}
		}
	}

	r.mu.Lock()
	if existing, ok := r.staged[handle]; ok {
		r.mu.Unlock()
		return existing, nil
	}
	if r.totalStagedBytes+int64(len(data)) > maxSessionKBStagedBytes {
		used := r.totalStagedBytes
		r.mu.Unlock()
		return "", fmt.Errorf("session KB staging quota exceeded (%d/%d bytes); reuse already staged files",
			used+int64(len(data)), maxSessionKBStagedBytes)
	}
	r.mu.Unlock()

	targetPath := buildKBStagePath(r.inputDir, handle, origName, mimeType, isImage)
	if err := r.sandboxStore.WriteSessionInputFile(ctx, r.sessionID, targetPath, data); err != nil {
		return "", fmt.Errorf("stage into sandbox: %w", err)
	}

	r.mu.Lock()
	if r.staged == nil {
		r.staged = make(map[string]string)
	}
	if _, already := r.staged[handle]; !already {
		r.staged[handle] = targetPath
		r.totalStagedBytes += int64(len(data))
	}
	r.mu.Unlock()
	return targetPath, nil
}

func buildKBStagePath(inputDir, handle, origName, mimeType string, isImage bool) string {
	kbDir := path.Join(path.Clean(inputDir), kbSandboxInputSubdir)
	ext := inferKBResourceExt(origName, mimeType)
	if isImage {
		if ext == "" {
			ext = ".png"
		}
		return path.Join(kbDir, handle+ext)
	}
	safeName := sanitizeKBStageFileName(origName, ext)
	if safeName == "" {
		return path.Join(kbDir, handle+ext)
	}
	return path.Join(kbDir, handle+"_"+safeName)
}

func inferKBResourceExt(origName, mimeType string) string {
	if ext := strings.ToLower(path.Ext(strings.TrimSpace(origName))); ext != "" && len(ext) <= 12 {
		valid := true
		for _, ch := range ext[1:] {
			if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) {
				valid = false
				break
			}
		}
		if valid {
			return ext
		}
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/svg+xml":
		return ".svg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tiff"
	case "application/pdf":
		return ".pdf"
	case "text/csv":
		return ".csv"
	case "text/markdown", "text/markdown; charset=utf-8":
		return ".md"
	case "text/html", "text/html; charset=utf-8":
		return ".html"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

func sanitizeKBStageFileName(origName, fallbackExt string) string {
	base := path.Base(strings.ReplaceAll(strings.TrimSpace(origName), "\\", "/"))
	if base == "" || base == "." || base == "/" || base == ".." {
		return ""
	}
	ext := strings.ToLower(path.Ext(base))
	stem := strings.TrimSuffix(base, path.Ext(base))
	var b strings.Builder
	for _, r := range stem {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteByte('_')
		case unicode.IsControl(r):
			continue
		case unicode.IsSpace(r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	cleanedStem := strings.Trim(b.String(), "._ ")
	if runes := []rune(cleanedStem); len(runes) > 48 {
		cleanedStem = string(runes[:48])
	}
	if ext == "" {
		ext = fallbackExt
	}
	if cleanedStem == "" {
		return ""
	}
	return cleanedStem + ext
}

func isTIFFImage(mimeType, origName string) bool {
	if strings.Contains(strings.ToLower(mimeType), "tiff") {
		return true
	}
	ext := strings.ToLower(path.Ext(strings.TrimSpace(origName)))
	return ext == ".tif" || ext == ".tiff"
}

func transcodeTIFFToPNG(data []byte) ([]byte, error) {
	img, err := tiff.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// registerKnowledgeResourceReader attaches authorized KB resource reading (resource://<handle>)
// to read_file whenever the agent has an active knowledge scope.
func (s *agentService) registerKnowledgeResourceReader(
	ctx context.Context,
	registry *tools.ToolRegistry,
	config *types.AgentConfig,
	sessionID string,
) {
	if s == nil || s.db == nil || s.knowledgeService == nil || s.fileService == nil ||
		config == nil || config.SkillInstallMode() || !agentHasKnowledgeScope(config) {
		return
	}

	var reader *tools.ReadFileTool
	if existing, err := registry.GetTool(tools.ToolReadFile); err == nil {
		if reader, _ = existing.(*tools.ReadFileTool); reader == nil {
			logger.Warnf(ctx, "Cannot attach KB resource reader: read_file is registered by another tool")
			return
		}
	} else {
		reader = tools.NewReadFileTool(nil)
		registry.RegisterTool(reader)
	}

	var store sandbox.SessionFileStore
	inputDir := ""
	if sessionID != "" {
		if sandboxMgr, err := s.resolveWorkspaceSandbox(ctx, sessionID, config); err == nil && sandboxMgr != nil {
			if candidate := sessionSandboxFileStore(sandboxMgr); candidate != nil {
				layout := sandbox.RemoteWorkspaceLayout()
				if describer, ok := candidate.(interface {
					SessionWorkspaceLayout(context.Context, string) (sandbox.WorkspaceLayout, error)
				}); ok {
					if described, lErr := describer.SessionWorkspaceLayout(ctx, sessionID); lErr == nil {
						layout = described
					}
				}
				if strings.TrimSpace(layout.InputDir) != "" {
					store = candidate
					inputDir = layout.InputDir
				}
			}
		}
	}

	reader.WithKnowledgeResources(&kbResourceReader{
		lookup:           &gormKBResourceLookup{db: s.db},
		knowledgeService: s.knowledgeService,
		fileService:      s.fileService,
		searchTargets:    config.SearchTargets,
		sessionID:        sessionID,
		sandboxStore:     store,
		inputDir:         inputDir,
	})
}
