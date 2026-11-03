package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

type imQAConfigLoader struct{ config *types.TenantSandboxConfig }

func (l imQAConfigLoader) Load(_ context.Context, tenant uint64, id string) (sandbox.ResolvedTenantSandboxConfig, error) {
	return sandbox.ResolvedTenantSandboxConfig{Config: l.config, Found: tenant == 7 && id == "cfg"}, nil
}

// This verifier is intentionally fake: runtime disk quotas are not proven by
// the mocked Docker Engine or by this integration test.
type imQADiskVerifier struct{}

func (imQADiskVerifier) VerifyIMSandboxDisk(context.Context, uint64, string, string, container.InspectResponse) error {
	return nil
}

type imQAChecker struct{}

func (imQAChecker) SessionExists(_ context.Context, key sandbox.SessionSandboxKey) (bool, error) {
	return key.TenantID == 7 && key.SessionID == "s-1", nil
}

type imQAMessages struct{ interfaces.MessageRepository }

func (imQAMessages) GetSessionAttachments(context.Context, string) (types.MessageAttachments, error) {
	return nil, nil
}

type imQAModelService struct {
	interfaces.ModelService
	model chat.Chat
}

func (m imQAModelService) GetModelByID(context.Context, string) (*types.Model, error) {
	return &types.Model{ID: "fake", Type: types.ModelTypeKnowledgeQA}, nil
}
func (m imQAModelService) GetChatModel(context.Context, string) (chat.Chat, error) {
	return m.model, nil
}

type imQAModel struct {
	calls       atomic.Int64
	offered     atomic.Bool
	contextLost atomic.Bool
	leaked      atomic.Bool
	onTool      func()
}

func (*imQAModel) Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}
func (m *imQAModel) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	for _, message := range messages {
		if strings.Contains(message.Content, "must-not-stream-after-revoke") {
			m.leaked.Store(true)
		}
	}
	if grant, ok := types.IMSandboxGrantFromContext(ctx); !ok || !grant.AllowShell || types.IMSandboxPolicyFromContext(ctx) == "" {
		m.contextLost.Store(true)
	}
	offered := false
	for _, tool := range opts.Tools {
		if tool.Function.Name == tools.ToolShellExec {
			offered = true
		}
	}
	m.offered.Store(offered)
	call := m.calls.Add(1)
	out := make(chan types.StreamResponse, 1)
	if offered && call == 1 {
		if m.onTool != nil {
			m.onTool()
		}
		out <- types.StreamResponse{ResponseType: types.ResponseTypeAnswer, ToolCalls: []types.LLMToolCall{{ID: "call", Type: "function", Function: types.FunctionCall{Name: tools.ToolShellExec, Arguments: `{"command":"printf octo-qa-marker"}`}}}, Done: true, FinishReason: "tool_calls"}
	} else {
		out <- types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Content: "done", Done: true, FinishReason: "stop"}
	}
	close(out)
	return out, nil
}
func (*imQAModel) GetModelName() string { return "fake" }
func (*imQAModel) GetModelID() string   { return "fake" }

type imQADocker struct {
	mu            sync.Mutex
	image         string
	cfg           *container.Config
	host          *container.HostConfig
	running       bool
	next          int
	commands      map[string]string
	marker        atomic.Int64
	stops         atomic.Int64
	nofollowSeen  atomic.Bool
	blockMarker   bool
	markerStarted chan struct{}
	stopped       chan struct{}
	stopOnce      sync.Once
}

func (d *imQADocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.Contains(path, "/_ping") {
		w.Header().Set("API-Version", "1.44")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("OK"))
		return
	}
	if strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json") {
		writeIMJSON(w, map[string]any{"Id": d.image, "Config": map[string]any{"Env": []string{}}})
		return
	}
	if strings.HasSuffix(path, "/containers/create") {
		var body struct {
			container.Config
			HostConfig *container.HostConfig
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		d.mu.Lock()
		d.cfg = &body.Config
		d.host = body.HostConfig
		d.mu.Unlock()
		writeIMJSON(w, map[string]any{"Id": "c1", "Warnings": []string{}})
		return
	}
	if strings.HasSuffix(path, "/containers/c1/start") {
		d.mu.Lock()
		d.running = true
		d.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	if strings.HasSuffix(path, "/containers/c1/stop") {
		d.stops.Add(1)
		d.mu.Lock()
		d.running = false
		stopped := d.stopped
		d.mu.Unlock()
		if stopped != nil {
			d.stopOnce.Do(func() { close(stopped) })
		}
		w.WriteHeader(204)
		return
	}
	if strings.HasSuffix(path, "/containers/c1/json") {
		d.mu.Lock()
		status := "exited"
		if d.running {
			status = "running"
		}
		response := container.InspectResponse{ID: "c1", Image: d.image, Config: d.cfg, HostConfig: d.host, State: &container.State{Status: container.ContainerState(status), Running: d.running}}
		d.mu.Unlock()
		writeIMJSON(w, response)
		return
	}
	if strings.HasSuffix(path, "/containers/c1/exec") {
		var body struct{ Cmd []string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		cmd := strings.Join(body.Cmd, " ")
		d.mu.Lock()
		d.next++
		id := fmt.Sprintf("e%d", d.next)
		d.commands[id] = cmd
		d.mu.Unlock()
		if strings.Contains(cmd, "octo-qa-marker") {
			d.marker.Add(1)
		}
		writeIMJSON(w, map[string]any{"Id": id})
		return
	}
	if strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/start") {
		parts := strings.Split(path, "/")
		id := parts[len(parts)-2]
		d.mu.Lock()
		cmd := d.commands[id]
		block := d.blockMarker && strings.Contains(cmd, "octo-qa-marker")
		started, stopped := d.markerStarted, d.stopped
		d.mu.Unlock()
		payload := ""
		if strings.Contains(cmd, "os.scandir") || strings.Contains(cmd, "os.walk") {
			payload = "[]"
		}
		if strings.Contains(cmd, "before.st_size") {
			if strings.Contains(cmd, "os.O_NOFOLLOW") {
				d.nofollowSeen.Store(true)
			}
			payload = strings.Repeat("x", 512*1024)
		}
		if strings.Contains(cmd, "octo-qa-marker") {
			payload = "reached"
			if block {
				payload = "must-not-stream-after-revoke"
			}
		}
		_, _ = io.Copy(io.Discard, r.Body)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		if payload != "" {
			header := make([]byte, 8)
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
			_, _ = buf.Write(header)
			_, _ = buf.WriteString(payload)
		}
		_ = buf.Flush()
		if block {
			close(started)
			select {
			case <-stopped:
			case <-time.After(10 * time.Second):
			}
		}
		// Consume the client's half-close before closing, avoiding a TCP reset
		// that would mask a successful Docker stream on Windows.
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, buf)
		return
	}
	if strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json") {
		writeIMJSON(w, map[string]any{"Running": false, "ExitCode": 0})
		return
	}
	if strings.HasSuffix(path, "/containers/json") {
		writeIMJSON(w, []any{})
		return
	}
	http.Error(w, "unexpected "+r.Method+" "+path, 404)
}
func writeIMJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func startIMQADocker(t *testing.T, image string) (*imQADocker, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated fake Docker"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	dir := t.TempDir()
	for name, data := range map[string][]byte{"ca.pem": certPEM, "cert.pem": certPEM, "key.pem": keyPEM} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0600))
	}
	daemon := &imQADocker{image: image, commands: map[string]string{}}
	server := httptest.NewUnstartedServer(daemon)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return daemon, "tcp://" + strings.TrimPrefix(server.URL, "https://"), dir
}

func TestIMAgentQAEndToEndShellAndRevocation(t *testing.T) {
	if err := sandbox.RequireIMBoundedOutput(); err != nil {
		t.Skip("bounded transport is supplied by the artifact integration")
	}
	image := "sha256:" + strings.Repeat("a", 64)
	t.Setenv(sandbox.IMTrustedImageEnv, image)
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	daemon, host, certs := startIMQADocker(t, image)
	tenantConfig := &types.TenantSandboxConfig{SandboxType: "docker", AllowPrivateEndpoints: true, Docker: &types.DockerSandboxConfig{Image: "base", Host: host, TLSCertPath: certs}}
	db := newPinTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.TenantSandboxConfigEntity{}, &types.Message{}))
	require.NoError(t, db.Create(&types.TenantSandboxConfigEntity{ID: "cfg", TenantID: 7, Name: "test", SandboxType: "docker", Config: tenantConfig}).Error)
	pinner := NewSessionSandboxPinner(db)
	resolver, err := sandbox.NewTenantSandboxResolver(sandbox.TenantSandboxResolverDeps{GlobalConfig: sandbox.DefaultConfig(), Loader: imQAConfigLoader{tenantConfig}, Store: sandbox.NewMemorySessionSandboxBindingStore(), Checker: imQAChecker{}, IMDiskVerifier: imQADiskVerifier{}})
	require.NoError(t, err)
	appConfig := &config.Config{}
	agentSvc := &agentService{cfg: appConfig, db: db, sandboxResolver: resolver, sandboxPinner: pinner}
	model := &imQAModel{}
	sessionSvc := &sessionService{cfg: appConfig, modelService: imQAModelService{model: model}, messageRepo: imQAMessages{}, agentService: agentSvc, sandboxResolver: resolver, sandboxPinner: pinner}
	var revoked atomic.Bool
	grant := types.IMSandboxGrant{SessionID: "s-1", TenantID: 7, ConfigID: "cfg", AgentConfigDigest: strings.Repeat("a", 64), AllowShell: true, Revalidate: func(ctx context.Context) error {
		principal, ok := types.PrincipalFromContext(ctx)
		sid, has := types.SessionIDFromContext(ctx)
		if revoked.Load() || !ok || principal.ID != "native" || !has || sid != "s-1" {
			return types.ErrIMSandboxDenied
		}
		return nil
	}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: 7})
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: "native"})
	ctx = types.WithIMKnowledgeScope(ctx, nil)
	ctx = types.WithIMSandboxRequests(ctx, true, false)
	ctx = types.WithIMSandboxGrant(ctx, grant)
	var streamed atomic.Int64
	ctx = sandbox.WithCommandOutput(ctx, func(_ string, data []byte) { streamed.Add(int64(len(data))) })
	agent := &types.CustomAgent{ID: "agent", TenantID: 7, Config: types.CustomAgentConfig{ModelID: "fake", WebSearchProviderID: "unused", KBSelectionMode: "none", SkillsSelectionMode: "none", SandboxConfigID: "cfg", AllowedTools: []string{"__no_scoped_tools__"}, MaxIterations: 3}}
	req := &types.QARequest{Session: &types.Session{ID: "s-1", TenantID: 7}, CustomAgent: agent, Query: "test", AssistantMessageID: "message", TurnLeaseHeld: true}
	// The artifact collector prepares this same context before AgentQA.
	prepared, err := sessionSvc.PrepareIMSandboxContext(ctx, true)
	require.NoError(t, err)
	// No production disk verifier is wired: execution must fail before pin or
	// container creation. The other resolver below uses only a fake verifier.
	noQuota, err := sandbox.NewTenantSandboxResolver(sandbox.TenantSandboxResolverDeps{GlobalConfig: sandbox.DefaultConfig(), Loader: imQAConfigLoader{tenantConfig}, Store: sandbox.NewMemorySessionSandboxBindingStore(), Checker: imQAChecker{}})
	require.NoError(t, err)
	_, _, err = resolveSandboxForExecution(prepared, noQuota, nil, pinner, 7, "s-1", "cfg", nil)
	require.ErrorIs(t, err, sandbox.ErrIMDiskLimitUnavailable)
	daemon.mu.Lock()
	require.Nil(t, daemon.cfg, "missing quota must not create a container")
	daemon.mu.Unlock()
	pin, err := pinner.Read(prepared, "s-1")
	require.NoError(t, err)
	require.True(t, pin.IsZero())
	require.NoError(t, ReconcileIMSandboxBeforeTurn(prepared, pinner, resolver, "s-1"))
	require.NoError(t, ValidateIMSandboxEmptyBaseline(prepared, pinner, resolver, "s-1"))
	manager, err := resolver.Resolve(prepared, 7, "cfg")
	require.NoError(t, err)
	_, err = manager.(*sandbox.SessionBoundManager).EnsureIMSession(prepared, "s-1", false)
	require.ErrorIs(t, err, sandbox.ErrIMSandboxNotFound)
	ctx = prepared
	qaErr := sessionSvc.AgentQA(ctx, req, event.NewEventBus())
	if qaErr != nil {
		daemon.mu.Lock()
		t.Logf("fake docker host: %+v", daemon.host)
		daemon.mu.Unlock()
	}
	require.NoError(t, qaErr)
	require.True(t, model.offered.Load())
	require.False(t, model.contextLost.Load())
	require.Equal(t, int64(1), daemon.marker.Load())
	// A missing SQL pin does not erase the underlying Redis/local binding.
	require.NoError(t, pinner.Clear(ctx, "s-1"))
	daemon.mu.Lock()
	daemon.running = true
	daemon.mu.Unlock()
	beforeStops := daemon.stops.Load()
	require.ErrorIs(t, ReconcileIMSandboxBeforeTurn(ctx, pinner, resolver, "s-1"), ErrIMSandboxBaselineNotEmpty)
	require.Greater(t, daemon.stops.Load(), beforeStops, "an old bound container must be stopped before rejecting the baseline")
	require.ErrorIs(t, ValidateIMSandboxEmptyBaseline(ctx, pinner, resolver, "s-1"), ErrIMSandboxBaselineNotEmpty)
	_, err = pinner.Pin(ctx, "s-1", SandboxPin{ConfigID: "cfg", TenantID: 7})
	require.NoError(t, err)
	_, err = sessionSvc.PrepareIMSandboxContext(ctx, false)
	require.NoError(t, err)
	// Exercise the parent's NOFOLLOW bounded Docker reader through the real
	// guarded manager with an artifact larger than the normal tool cap.
	guarded, _, err := resolveSandboxForExecution(ctx, resolver, nil, pinner, 7, "s-1", "cfg", nil)
	require.NoError(t, err)
	bounded, ok := guarded.(interface {
		ReadSessionFileBounded(context.Context, string, string, int64) ([]byte, error)
	})
	require.True(t, ok)
	data, err := bounded.ReadSessionFileBounded(ctx, "s-1", "/workspace/output/large.docx", 1<<20)
	require.NoError(t, err)
	require.Len(t, data, 512*1024)
	require.True(t, daemon.nofollowSeen.Load())
	_, err = bounded.ReadSessionFileBounded(ctx, "s-1", "/workspace/output/../private", 1<<20)
	require.Error(t, err)
	_, err = bounded.ReadSessionFileBounded(ctx, "s-1", "/workspace/output/large.docx", (20<<20)+1)
	require.Error(t, err)

	// Revocation between the model response and tool execution must stop
	// the actual shell call, even though the same sandbox is already pinned.
	midTurn := &imQAModel{onTool: func() { revoked.Store(true) }}
	sessionSvc.modelService = imQAModelService{model: midTurn}
	require.NoError(t, sessionSvc.AgentQA(ctx, req, event.NewEventBus()))
	require.Equal(t, int64(1), daemon.marker.Load())
	// Let a real AgentQA command reach the mocked Docker stream, then revoke.
	revoked.Store(false)
	daemon.mu.Lock()
	daemon.blockMarker = true
	daemon.markerStarted = make(chan struct{})
	daemon.stopped = make(chan struct{})
	started, stopped := daemon.markerStarted, daemon.stopped
	daemon.mu.Unlock()
	longModel := &imQAModel{}
	sessionSvc.modelService = imQAModelService{model: longModel}
	qaDone := make(chan error, 1)
	go func() { qaDone <- sessionSvc.AgentQA(ctx, req, event.NewEventBus()) }()
	select {
	case <-started:
	case <-time.After(8 * time.Second):
		t.Fatal("running command did not reach Docker")
	}
	revoked.Store(true)
	select {
	case <-stopped:
	case <-time.After(8 * time.Second):
		t.Fatal("revoked container was not stopped")
	}
	select {
	case <-qaDone:
	case <-time.After(8 * time.Second):
		t.Fatal("revoked turn did not finish")
	}
	require.Zero(t, streamed.Load(), "live shell bytes escaped before revalidation")
	require.False(t, longModel.leaked.Load(), "revoked output reached a later model call")
	require.Equal(t, int64(2), daemon.marker.Load())
	err = sessionSvc.AgentQA(ctx, req, event.NewEventBus())
	require.Error(t, err)
	require.Equal(t, int64(2), daemon.marker.Load())
	// The old pin grants nothing when the channel switch is off.
	offModel := &imQAModel{}
	sessionSvc.modelService = imQAModelService{model: offModel}
	offCtx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	offCtx = context.WithValue(offCtx, types.TenantInfoContextKey, &types.Tenant{ID: 7})
	offCtx = types.WithPrincipal(offCtx, types.Principal{Type: types.PrincipalIMUser, ID: "native"})
	offCtx = types.WithIMKnowledgeScope(offCtx, nil)
	offAgent := *agent
	offAgent.Config = agent.Config
	offAgent.Config.SandboxConfigID = ""
	offReq := *req
	offReq.CustomAgent = &offAgent
	require.NoError(t, sessionSvc.AgentQA(offCtx, &offReq, event.NewEventBus()))
	require.False(t, offModel.offered.Load())
	require.Equal(t, int64(2), daemon.marker.Load())
}
