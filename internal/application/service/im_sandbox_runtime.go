package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

// Resolve only the admitted same-tenant Docker config. A saved pin is a
// storage binding, never a permission; mismatch and read errors fail closed.
func resolveIMSandboxForExecution(
	ctx context.Context,
	resolver sandbox.TenantSandboxResolver,
	fallback sandbox.Manager,
	pinner *SessionSandboxPinner,
	tenantID uint64,
	sessionID,
	configID string,
	policy WorkspaceSandboxPolicy,
	opts ...resolveOption,
) (sandbox.Manager, SandboxPin, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && !grant.AllowSkills) {
		return sandbox.NewDisabledManager(), SandboxPin{}, nil
	}
	if applyResolveOptions(opts).liteOnly ||
		tenantID != grant.TenantID ||
		sessionID != grant.SessionID ||
		strings.TrimSpace(configID) != grant.ConfigID ||
		pinner == nil {
		return nil, SandboxPin{}, types.ErrIMSandboxDenied
	}
	if _, err := ValidateIMSandboxPin(ctx, pinner, sessionID, true); err != nil {
		return nil, SandboxPin{}, err
	}
	baseline, ok := ctx.Value(imSandboxConfigBaselineKey{}).([32]byte)
	if !ok {
		return nil, SandboxPin{}, errors.New("octo sandbox: configuration baseline unavailable")
	}
	current, err := imSandboxConfigFingerprint(ctx, pinner, grant)
	if err != nil || current != baseline {
		return nil, SandboxPin{}, types.ErrIMSandboxDenied
	}
	if policy != nil {
		disabled, err := policy.WorkspaceScriptsDisabled(ctx, tenantID)
		if err != nil || disabled {
			return nil, SandboxPin{}, types.ErrIMSandboxDenied
		}
	}
	mgr, err := resolveTenantSandboxForConfig(ctx, resolver, fallback, tenantID, grant.ConfigID, nil)
	if err != nil {
		return nil, SandboxPin{}, err
	}
	backend, ok := mgr.(*sandbox.SessionBoundManager)
	if !ok || backend.GetType() != sandbox.SandboxTypeDocker {
		return nil, SandboxPin{}, errors.New("octo sandbox: isolated Docker manager required")
	}
	if err := backend.RequireIMDiskIsolation(); err != nil {
		return nil, SandboxPin{}, err
	}
	winner, err := pinner.Pin(ctx, sessionID, SandboxPin{ConfigID: grant.ConfigID, TenantID: grant.TenantID})
	if err != nil || winner.ConfigID != grant.ConfigID || winner.TenantOr(grant.TenantID) != grant.TenantID {
		return nil, SandboxPin{}, types.ErrIMSandboxDenied
	}
	if _, err := ValidateIMSandboxPin(ctx, pinner, sessionID, false); err != nil {
		return nil, SandboxPin{}, err
	}
	return &imGuardedSandbox{backend: backend, pinner: pinner, configFingerprint: baseline}, winner, nil
}

func imSandboxConfigFingerprint(
	ctx context.Context,
	pinner *SessionSandboxPinner,
	grant types.IMSandboxGrant,
) ([32]byte, error) {
	cfg, err := repository.NewTenantSandboxConfigRepository(pinner.db).GetByID(ctx, grant.TenantID, grant.ConfigID)
	if err != nil || cfg == nil || cfg.Config == nil {
		return [32]byte{}, types.ErrIMSandboxDenied
	}
	raw, err := json.Marshal(cfg.Config)
	if err != nil {
		return [32]byte{}, types.ErrIMSandboxDenied
	}
	return sha256.Sum256(raw), nil
}

type (
	imSandboxConfigBaselineKey struct{}
	imSandboxFreshTurnKey      struct{}
)

// IsIMSandboxFreshTurn reports that the lock holder stopped the previous
// session container before beginning this turn.
func IsIMSandboxFreshTurn(ctx context.Context) bool {
	fresh, _ := ctx.Value(imSandboxFreshTurnKey{}).(bool)
	return fresh
}

// PrepareIMSandboxTurn must run while holding the IM session turn lock.
// Stopping the old container discards its tmpfs before any new work starts.
func (s *sessionService) PrepareIMSandboxTurn(ctx context.Context) (context.Context, error) {
	prepared, err := s.PrepareIMSandboxContext(ctx, true)
	if err != nil {
		return nil, err
	}
	grant, _ := types.IMSandboxGrantFromContext(prepared)
	if err := ReconcileIMSandboxBeforeTurn(prepared, s.sandboxPinner, s.sandboxResolver, grant.SessionID); err != nil {
		return nil, err
	}
	return context.WithValue(prepared, imSandboxFreshTurnKey{}, true), nil
}

func withIMSandboxConfigBaseline(ctx context.Context, pinner *SessionSandboxPinner) (context.Context, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && !grant.AllowSkills) {
		return ctx, nil
	}
	if pinner == nil || pinner.db == nil {
		return nil, types.ErrIMSandboxDenied
	}
	if _, err := ValidateIMSandboxPin(ctx, pinner, grant.SessionID, true); err != nil {
		return nil, err
	}
	fingerprint, err := imSandboxConfigFingerprint(ctx, pinner, grant)
	if err != nil {
		return nil, err
	}
	if previous, ok := ctx.Value(imSandboxConfigBaselineKey{}).([32]byte); ok && previous != fingerprint {
		return nil, types.ErrIMSandboxDenied
	}
	return context.WithValue(ctx, imSandboxConfigBaselineKey{}, fingerprint), nil
}

// Each turn gets a policy identity. Existing containers with a different
// Agent/config/Skill snapshot must never be reused after revocation.
type imSkillFingerprintKey struct{}

func withIMSandboxPolicy(ctx context.Context) (context.Context, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && !grant.AllowSkills) {
		return ctx, nil
	}
	config, ok := ctx.Value(imSandboxConfigBaselineKey{}).([32]byte)
	if !ok || grant.AgentConfigDigest == "" {
		return nil, types.ErrIMSandboxDenied
	}
	var skills [32]byte
	if grant.AllowSkills {
		var hasSkills bool
		skills, hasSkills = ctx.Value(imSkillFingerprintKey{}).([32]byte)
		if !hasSkills {
			return nil, types.ErrIMSandboxDenied
		}
	}
	material, err := json.Marshal(struct {
		Tenant                   uint64
		Session, ConfigID, Agent string
		Config, Skills           [32]byte
		Shell, Skill             bool
	}{
		grant.TenantID,
		grant.SessionID,
		grant.ConfigID,
		grant.AgentConfigDigest,
		config,
		skills,
		grant.AllowShell,
		grant.AllowSkills,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(material)
	next := fmt.Sprintf("%x", digest)
	if previous := types.IMSandboxPolicyFromContext(ctx); previous != "" && previous != next {
		return nil, types.ErrIMSandboxDenied
	}
	return types.WithIMSandboxPolicy(ctx, next), nil
}

// PrepareIMSandboxContext freezes and revalidates the native grant, sandbox
// config and selected Skill snapshot without creating a pin or container.
// Artifact begin may allow an absent pin; every later read must pass false.
// The returned context must be retained for the entire turn.
func (s *sessionService) PrepareIMSandboxContext(ctx context.Context, allowUnpinned bool) (context.Context, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && !grant.AllowSkills) {
		return nil, errors.New("octo sandbox: execution authorization unavailable")
	}
	if sid, exists := types.SessionIDFromContext(ctx); exists && sid != grant.SessionID {
		return nil, types.ErrIMSandboxDenied
	}
	ctx = types.WithSessionID(ctx, grant.SessionID)
	if _, err := ValidateIMSandboxPin(ctx, s.sandboxPinner, grant.SessionID, allowUnpinned); err != nil {
		return nil, err
	}
	ctx, err := withIMSandboxConfigBaseline(ctx, s.sandboxPinner)
	if err != nil {
		return nil, err
	}
	if grant.AllowSkills {
		ctx, err = s.withIMSkillsCheck(ctx, &types.AgentConfig{SkillsEnabled: true})
		if err != nil {
			return nil, err
		}
	}
	ctx, err = withIMSandboxPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if err = sandbox.RequireIMBoundedOutput(); err != nil {
		return nil, err
	}
	return ctx, nil
}

// ReconcileIMSandboxBeforeTurn confirms the old bound container is stopped
// before snapshot or execution. The caller must hold a renewable, fenced turn
// lock; a lost lease or unresolved prior daemon must quarantine the session.
// An empty DB pin plus an existing binding is stopped but remains an error.
func ReconcileIMSandboxBeforeTurn(
	ctx context.Context,
	pinner *SessionSandboxPinner,
	resolver sandbox.TenantSandboxResolver,
	sessionID string,
) error {
	grant, err := ValidateIMSandboxPin(ctx, pinner, sessionID, true)
	if err != nil {
		return err
	}
	if resolver == nil {
		return errors.New("Octo sandbox: binding resolver unavailable")
	}
	pin, err := pinner.Read(ctx, sessionID)
	if err != nil {
		return err
	}
	manager, err := resolver.Resolve(ctx, grant.TenantID, grant.ConfigID)
	if err != nil {
		return err
	}
	backend, ok := manager.(*sandbox.SessionBoundManager)
	if !ok || backend.GetType() != sandbox.SandboxTypeDocker {
		return errors.New("Octo sandbox: isolated Docker manager required")
	}
	_, err = backend.QuiesceIMSession(ctx, sessionID)
	if errors.Is(err, sandbox.ErrIMSandboxNotFound) {
		// A pin can be saved before the first command creates a container.
		// A successful authoritative lookup proving no binding is safe.
		return nil
	}
	if err != nil {
		return err
	}
	if pin.IsZero() {
		return ErrIMSandboxBaselineNotEmpty
	}
	return nil
}

// ErrIMSandboxBaselineNotEmpty means a pin or underlying session binding
// exists; callers must not treat its previous output as an empty baseline.
//
//nolint:staticcheck // Preserve existing error text.
var ErrIMSandboxBaselineNotEmpty = errors.New("Octo sandbox: existing session binding prevents an empty baseline")

// ValidateIMSandboxEmptyBaseline permits an empty pre-turn snapshot only when
// both the DB pin and authoritative sandbox binding are definitely absent.
// ctx must be prepared by PrepareIMSandboxContext; this function never pins,
// creates, reads files, destroys a container, or substitutes another backend.
func ValidateIMSandboxEmptyBaseline(
	ctx context.Context,
	pinner *SessionSandboxPinner,
	resolver sandbox.TenantSandboxResolver,
	sessionID string,
) error {
	grant, err := ValidateIMSandboxPin(ctx, pinner, sessionID, true)
	if err != nil {
		return err
	}
	if resolver == nil {
		return errors.New("Octo sandbox: binding resolver unavailable")
	}
	pin, err := pinner.Read(ctx, sessionID)
	if err != nil {
		return err
	}
	if !pin.IsZero() {
		return ErrIMSandboxBaselineNotEmpty
	}
	manager, err := resolver.Resolve(ctx, grant.TenantID, grant.ConfigID)
	if err != nil {
		return err
	}
	backend, ok := manager.(*sandbox.SessionBoundManager)
	if !ok || backend.GetType() != sandbox.SandboxTypeDocker {
		return errors.New("Octo sandbox: isolated Docker manager required")
	}
	_, err = backend.EnsureIMSession(ctx, sessionID, false)
	if errors.Is(err, sandbox.ErrIMSandboxNotFound) {
		// Re-read the SQL state after the binding lookup; a concurrently
		// deleted session or newly written pin is never an empty baseline.
		if _, err = ValidateIMSandboxPin(ctx, pinner, sessionID, true); err != nil {
			return err
		}
		pin, err = pinner.Read(ctx, sessionID)
		if err != nil {
			return err
		}
		if !pin.IsZero() {
			return ErrIMSandboxBaselineNotEmpty
		}
		return nil
	}
	if err == nil {
		return ErrIMSandboxBaselineNotEmpty
	}
	return err
}

type (
	imSkillRevalidatorKey struct{}
	imSkillRevalidator    func(context.Context) error
)

func withIMSkillRevalidator(ctx context.Context, check imSkillRevalidator) context.Context {
	return context.WithValue(ctx, imSkillRevalidatorKey{}, check)
}

type imSandboxBackend interface {
	sandbox.SessionFileStore
	EnsureIMSession(context.Context, string, bool) (string, error)
	StopIMSession(context.Context, string, string) error
	ExecIMShell(
		context.Context,
		string,
		string,
		string,
		string,
		time.Duration,
		map[string]string,
	) (*sandbox.ExecuteResult, error)
	ListIMSessionFiles(context.Context, string, string) ([]sandbox.RemoteDirEntry, error)
}

type imGuardedSandbox struct {
	backend           imSandboxBackend
	pinner            *SessionSandboxPinner
	configFingerprint [32]byte
}

func (m *imGuardedSandbox) revalidate(ctx context.Context, sessionID string) error {
	grant, err := ValidateIMSandboxPin(ctx, m.pinner, sessionID, false)
	if err != nil {
		return err
	}
	fingerprint, err := imSandboxConfigFingerprint(ctx, m.pinner, grant)
	if err != nil || fingerprint != m.configFingerprint {
		return types.ErrIMSandboxDenied
	}
	if grant.AllowSkills {
		verifier, ok := ctx.Value(imSkillRevalidatorKey{}).(imSkillRevalidator)
		if !ok || verifier == nil {
			return errors.New("Octo sandbox: Skill authorization unavailable")
		}
		if err := verifier(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (m *imGuardedSandbox) guard(ctx context.Context, sessionID string) (context.Context, string, func(), error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || grant.TurnBarrier == nil {
		return nil, "", nil, types.ErrIMSandboxDenied
	}
	track, done, err := grant.TurnBarrier.Begin()
	if err != nil {
		return nil, "", nil, err
	}
	success := false
	defer func() {
		if !success {
			done()
		}
	}()
	if err = m.revalidate(ctx, sessionID); err != nil {
		return nil, "", nil, err
	}
	sandboxID, err := m.backend.EnsureIMSession(ctx, sessionID, true)
	if err != nil {
		return nil, "", nil, err
	}
	safe := sandbox.WithIMExpectedSandbox(ctx, sessionID, sandboxID)
	track(sandboxID, func(context.Context) error { return m.backend.StopIMSession(safe, sessionID, sandboxID) })
	if err = m.revalidate(ctx, sessionID); err != nil {
		return nil, "", nil, err
	}
	success = true
	return safe, sandboxID, done, nil
}

// A command may outlive its admission. Poll the current native grant, pin,
// Agent and Skill state; an error or a slow check cancels the command. The
// Docker backend stops and inspects the exact container on cancellation.
// This bounds detection latency but does not promise immediate revocation.
const (
	imRevalidationInterval = time.Second
	imRevalidationTimeout  = 2 * time.Second
)

func (m *imGuardedSandbox) withAuthorizationWatch(
	ctx context.Context,
	sid,
	id string,
	stopOnCancel bool,
	run func(context.Context) error,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(imRevalidationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				finished <- nil
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(runCtx, imRevalidationTimeout)
				checked := make(chan error, 1)
				go func() { checked <- m.revalidate(checkCtx, sid) }()
				var err error
				select {
				case err = <-checked:
				case <-checkCtx.Done():
					err = checkCtx.Err()
				}
				stop()
				if err != nil {
					if runCtx.Err() != nil {
						finished <- nil
						return
					}
					cancel()
					if stopOnCancel {
						if stopErr := m.backend.StopIMSession(ctx, sid, id); stopErr != nil {
							err = errors.Join(err, sandbox.ErrIMStopUnconfirmed)
						}
					}
					finished <- fmt.Errorf("Octo sandbox: authorization changed or could not be checked: %w", err)
					return
				}
			}
		}
	}()
	err := run(runCtx)
	cancel()
	if checkErr := <-finished; checkErr != nil {
		// Never expose a failed command's stderr after revocation. Preserve
		// only the fact that Docker termination was not confirmed.
		if errors.Is(err, sandbox.ErrIMStopUnconfirmed) {
			return errors.Join(checkErr, sandbox.ErrIMStopUnconfirmed)
		}
		return checkErr
	}
	if err != nil && stopOnCancel && ctx.Err() != nil {
		if stopErr := m.backend.StopIMSession(ctx, sid, id); stopErr != nil {
			err = errors.Join(err, sandbox.ErrIMStopUnconfirmed)
		}
	}
	if authErr := m.revalidate(ctx, sid); authErr != nil {
		if errors.Is(err, sandbox.ErrIMStopUnconfirmed) {
			return errors.Join(authErr, sandbox.ErrIMStopUnconfirmed)
		}
		return authErr
	}
	return err
}

func (m *imGuardedSandbox) execWatched(
	ctx context.Context,
	sid,
	id,
	command,
	workDir string,
	timeout time.Duration,
	env map[string]string,
) (*sandbox.ExecuteResult, error) {
	var result *sandbox.ExecuteResult
	err := m.withAuthorizationWatch(ctx, sid, id, true, func(runCtx context.Context) error {
		var err error
		result, err = m.backend.ExecIMShell(runCtx, sid, id, command, workDir, timeout, env)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (m *imGuardedSandbox) GetType() sandbox.SandboxType  { return sandbox.SandboxTypeDocker }
func (m *imGuardedSandbox) GetSandbox() sandbox.Sandbox   { return nil }
func (m *imGuardedSandbox) Cleanup(context.Context) error { return nil }
func (m *imGuardedSandbox) Execute(context.Context, *sandbox.ExecuteConfig) (*sandbox.ExecuteResult, error) {
	return nil, errors.New("Octo sandbox: use the guarded session execution capability")
}
func (m *imGuardedSandbox) SessionShellExecutor() sandbox.SessionShellExecutor { return m }
func (m *imGuardedSandbox) SessionFileStore() sandbox.SessionFileStore         { return m }
func (m *imGuardedSandbox) SessionWorkspaceLayout(context.Context, string) (sandbox.WorkspaceLayout, error) {
	return sandbox.RemoteWorkspaceLayout(), nil
}

func (m *imGuardedSandbox) ExecShellCommand(
	ctx context.Context,
	sid,
	command,
	workDir string,
	timeout time.Duration,
	env map[string]string,
) (*sandbox.ExecuteResult, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || !grant.AllowShell {
		return nil, types.ErrIMSandboxDenied
	}
	safe, id, done, err := m.guard(ctx, sid)
	if err != nil {
		return nil, err
	}
	defer done()
	return m.execWatched(safe, sid, id, command, workDir, timeout, env)
}

// ExecApprovedSkill is reachable only through the package-script tool.
func (m *imGuardedSandbox) ExecApprovedSkill(
	ctx context.Context,
	sid,
	command,
	workDir string,
	timeout time.Duration,
	env map[string]string,
) (*sandbox.ExecuteResult, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || !grant.AllowSkills {
		return nil, types.ErrIMSandboxDenied
	}
	safe, id, done, err := m.guard(ctx, sid)
	if err != nil {
		return nil, err
	}
	defer done()
	return m.execWatched(safe, sid, id, command, workDir, timeout, env)
}

func (m *imGuardedSandbox) EnsureSessionDir(ctx context.Context, sid, p string) error {
	safe, _, done, err := m.guard(ctx, sid)
	if err != nil {
		return err
	}
	defer done()
	return m.backend.EnsureSessionDir(safe, sid, p)
}

func (m *imGuardedSandbox) ListSessionFiles(ctx context.Context, sid, p string) ([]sandbox.RemoteDirEntry, error) {
	safe, id, done, err := m.guard(ctx, sid)
	if err != nil {
		return nil, err
	}
	defer done()
	var items []sandbox.RemoteDirEntry
	err = m.withAuthorizationWatch(safe, sid, id, false, func(runCtx context.Context) error {
		var e error
		items, e = m.backend.ListIMSessionFiles(runCtx, sid, p)
		return e
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (m *imGuardedSandbox) StatSessionFile(ctx context.Context, sid, p string) (*sandbox.RemoteStatEntry, error) {
	if len(p) > 4096 {
		return nil, errors.New("Octo sandbox: path is too long")
	}
	safe, id, done, err := m.guard(ctx, sid)
	if err != nil {
		return nil, err
	}
	defer done()
	var item *sandbox.RemoteStatEntry
	err = m.withAuthorizationWatch(safe, sid, id, true, func(runCtx context.Context) error {
		var e error
		item, e = m.backend.StatSessionFile(runCtx, sid, p)
		return e
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

// ReadSessionFile serves tools and bounded Skill integrity checks.
func (m *imGuardedSandbox) ReadSessionFile(ctx context.Context, sid, p string) ([]byte, error) {
	return m.readSessionFileBounded(ctx, sid, p, int64(sandbox.IMMaxOutputBytes), false)
}

// ReadSessionFileBounded is the artifact collector capability. It permits
// only a canonical path under this session's output root and requires the
// provider's component-by-component NOFOLLOW implementation. The caller must
// separately authorize file delivery immediately before publishing.
func (m *imGuardedSandbox) ReadSessionFileBounded(ctx context.Context, sid, p string, maxBytes int64) ([]byte, error) {
	return m.readSessionFileBounded(ctx, sid, p, maxBytes, true)
}

const imMaxFileDeliveryBytes int64 = 20 << 20

func (m *imGuardedSandbox) readSessionFileBounded(
	ctx context.Context,
	sid,
	p string,
	maxBytes int64,
	outputOnly bool,
) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > imMaxFileDeliveryBytes || len(p) > 4096 || strings.ContainsAny(p, "\\\x00") {
		return nil, errors.New("Octo sandbox: invalid bounded file read")
	}
	if outputOnly && (!path.IsAbs(p) || path.Clean(p) != p || !strings.HasPrefix(p, sandbox.SessionOutputRoot+"/")) {
		return nil, errors.New("Octo sandbox: file must be under this session's output directory")
	}
	safe, id, done, err := m.guard(ctx, sid)
	if err != nil {
		return nil, err
	}
	defer done()
	// Docker's raw stream cap includes framing; the provider independently
	// enforces maxBytes and no-follow resolution before returning content.
	safe, err = sandbox.WithIMDockerOutputLimit(safe, maxBytes+1)
	if err != nil {
		return nil, err
	}
	reader, ok := any(m.backend).(interface {
		ReadSessionFileBounded(context.Context, string, string, int64) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("Octo sandbox: bounded file reading is unavailable")
	}
	var data []byte
	err = m.withAuthorizationWatch(safe, sid, id, true, func(runCtx context.Context) error {
		var e error
		data, e = reader.ReadSessionFileBounded(runCtx, sid, p, maxBytes)
		return e
	})
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("Octo sandbox: file exceeded bounded read limit")
	}
	return data, nil
}

func (m *imGuardedSandbox) WriteSessionInputFile(ctx context.Context, sid, p string, data []byte) error {
	safe, _, done, err := m.guard(ctx, sid)
	if err != nil {
		return err
	}
	defer done()
	return m.backend.WriteSessionInputFile(safe, sid, p, data)
}

func (m *imGuardedSandbox) WriteSessionWorkspaceFile(ctx context.Context, sid, p string, data []byte) error {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || !grant.AllowShell {
		return types.ErrIMSandboxDenied
	}
	safe, _, done, err := m.guard(ctx, sid)
	if err != nil {
		return err
	}
	defer done()
	return m.backend.WriteSessionWorkspaceFile(safe, sid, p, data)
}

func (m *imGuardedSandbox) WriteSessionWorkspaceFiles(
	ctx context.Context,
	sid string,
	files []sandbox.SessionWorkspaceFile,
) error {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && (!grant.AllowSkills || !sandbox.IsIMSkillStaging(ctx))) {
		return types.ErrIMSandboxDenied
	}
	safe, _, done, err := m.guard(ctx, sid)
	if err != nil {
		return err
	}
	defer done()
	return m.backend.WriteSessionWorkspaceFiles(safe, sid, files)
}

func (m *imGuardedSandbox) RemoveSessionInputPath(ctx context.Context, sid, p string) error {
	safe, _, done, err := m.guard(ctx, sid)
	if err != nil {
		return err
	}
	defer done()
	return m.backend.RemoveSessionInputPath(safe, sid, p)
}

var (
	_ sandbox.Manager                   = (*imGuardedSandbox)(nil)
	_ sandbox.SessionCapabilityProvider = (*imGuardedSandbox)(nil)
	_ sandbox.SessionFileStore          = (*imGuardedSandbox)(nil)
)
