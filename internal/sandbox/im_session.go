package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const remoteMetadataIMPolicy = "weknora_im_policy"

// ErrIMSandboxNotFound means the binding was read successfully and is absent.
// No transport or database error is ever converted into this sentinel.
var ErrIMSandboxNotFound = errors.New("Octo sandbox: no live session sandbox")

// ErrIMStopUnconfirmed is never evidence of process termination.
var ErrIMStopUnconfirmed = errors.New("Octo sandbox: container stop could not be confirmed")

var ErrIMOutputLimitUnavailable = errors.New("Octo sandbox: bounded Docker output is unavailable")

type imExpectedBinding struct{ session, sandbox string }
type imExpectedBindingKey struct{}

// WithIMExpectedSandbox binds a guarded operation to the inspected container.
func WithIMExpectedSandbox(ctx context.Context, sessionID, sandboxID string) context.Context {
	return context.WithValue(ctx, imExpectedBindingKey{}, imExpectedBinding{sessionID, sandboxID})
}

func expectedIMSandbox(ctx context.Context, sessionID string) (string, error) {
	value, ok := ctx.Value(imExpectedBindingKey{}).(imExpectedBinding)
	if !ok || value.session != sessionID || value.sandbox == "" {
		return "", errors.New("Octo sandbox: inspected session binding required")
	}
	return value.sandbox, nil
}

// setIMOutputLimit bridges the bounded remote-exec capability supplied by the
// artifact integration branch. A build without that capability fails closed.
// Reflection avoids changing the shared provider transport in this branch.
func setIMOutputLimit(request any, limit int64) error {
	value := reflect.ValueOf(request)
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return ErrIMOutputLimitUnavailable
	}
	field := value.Elem().FieldByName("MaxOutputBytes")
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Int64 || limit <= 0 {
		return ErrIMOutputLimitUnavailable
	}
	field.SetInt(limit)
	return nil
}

// RequireIMBoundedOutput makes missing transport support a visible error.
func RequireIMBoundedOutput() error {
	return setIMOutputLimit(&RemoteExecRequest{}, IMMaxOutputBytes/2)
}

// RequireIMDiskIsolation is checked before pinning or provisioning.
func (m *SessionBoundManager) RequireIMDiskIsolation() error {
	if m == nil || m.dockerClient == nil {
		return ErrIMDiskLimitUnavailable
	}
	if _, ok := m.dockerClient.api.(*imLimitedDockerEngine); !ok {
		return ErrIMDiskLimitUnavailable
	}
	return nil
}

// EnsureIMSession creates only an absent binding, or validates an existing
// one without replacing/recovering it. It inspects the actual Docker object
// before returning the exact sandbox ID to be used for this operation.
func (m *SessionBoundManager) EnsureIMSession(ctx context.Context, sessionID string, create bool) (string, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || !types.HasIMKnowledgeScope(ctx) || (!grant.AllowShell && !grant.AllowSkills) || grant.SessionID != sessionID ||
		m == nil || m.remoteDisabled() || m.dockerClient == nil || m.GetType() != SandboxTypeDocker || m.config == nil ||
		m.lifecycle == nil || m.lifecycle.sandboxConfigID != grant.ConfigID || m.lifecycle.bootstrapper != nil ||
		m.config.DockerImage != strings.TrimSpace(getIMTrustedImage()) || m.config.DockerNetworkMode != "none" || len(m.config.EnvVars) != 0 {
		return "", errors.New("Octo sandbox: isolated Docker configuration unavailable")
	}
	if err := m.RequireIMDiskIsolation(); err != nil {
		return "", err
	}
	policy := types.IMSandboxPolicyFromContext(ctx)
	if !imPolicyID.MatchString(policy) || m.lifecycle.createRequest.Metadata[remoteMetadataIMPolicy] != policy {
		return "", errors.New("Octo sandbox: turn policy is unavailable or changed")
	}
	if err := setIMOutputLimit(&RemoteExecRequest{}, IMMaxOutputBytes/2); err != nil {
		return "", err
	}
	if err := grant.Revalidate(ctx); err != nil {
		return "", types.ErrIMSandboxDenied
	}
	key, err := m.sessionKey(ctx, sessionID)
	if err != nil || key.TenantID != grant.TenantID {
		return "", types.ErrIMSandboxDenied
	}
	var sandboxID string
	err = m.bindings.WithLifecycleLock(ctx, key, func(lockCtx context.Context) error {
		exists, err := m.checker.SessionExists(lockCtx, key)
		if err != nil {
			return err
		}
		if !exists {
			return ErrSandboxSessionDeleted
		}
		binding, err := m.lifecycle.readBinding(lockCtx, key)
		if err != nil {
			return err
		}
		if binding == nil {
			if !create {
				return ErrIMSandboxNotFound
			}
			if _, err = m.lifecycle.createAndBind(lockCtx, key); err != nil {
				return err
			}
			binding, err = m.lifecycle.readBinding(lockCtx, key)
			if err != nil {
				return err
			}
			if binding == nil {
				return errors.New("Octo sandbox: created binding not found")
			}
		}
		// A stale marker alone must not destroy or strand the old session. The
		// current image and full policy labels below still have to match.
		if binding.Provider != SandboxTypeDocker || binding.ConfigID != grant.ConfigID || binding.TemplateID != m.config.DockerImage {
			return errors.New("Octo sandbox: existing binding does not match the authorized clean image")
		}
		img, err := m.dockerClient.api.ImageInspect(lockCtx, m.config.DockerImage)
		if err != nil {
			return fmt.Errorf("Octo sandbox: inspect clean image: %w", err)
		}
		inspected, err := m.dockerClient.api.ContainerInspect(lockCtx, binding.SandboxID, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("Octo sandbox: inspect session container: %w", err)
		}
		if err = verifyIMDockerContainer(inspected.Container, img.InspectResponse, *binding, key, m.config.DockerImage, policy); err != nil {
			return err
		}
		if m.imDiskVerifier != nil {
			if err = m.imDiskVerifier.VerifyIMSandboxDisk(lockCtx, key.TenantID, key.SessionID, grant.ConfigID, inspected.Container); err != nil {
				return fmt.Errorf("%w: %v", ErrIMDiskLimitUnavailable, err)
			}
		}
		sandboxID = binding.SandboxID
		return nil
	})
	if err != nil {
		return "", err
	}
	if sandboxID == "" {
		return "", errors.New("Octo sandbox: inspected container unavailable")
	}
	return sandboxID, nil
}

func getIMTrustedImage() string { return strings.TrimSpace(getenvIMImage()) }

// getenvIMImage is a function so image-policy tests do not touch Docker.
func getenvIMImage() string { return strings.TrimSpace(os.Getenv(IMTrustedImageEnv)) }

func verifyIMDockerContainer(c container.InspectResponse, img image.InspectResponse, binding SessionSandboxBinding, key SessionSandboxKey, imageID, policy string) error {
	deny := func(reason string) error { return fmt.Errorf("Octo sandbox: unsafe container: %s", reason) }
	if !imImageID.MatchString(imageID) || img.ID != imageID || img.Config == nil || len(img.Config.Volumes) != 0 ||
		c.ID != binding.SandboxID || c.Image != imageID || c.Config == nil || c.HostConfig == nil {
		return deny("image or identity mismatch")
	}
	labels := c.Config.Labels
	if labels[dockerManagedLabel] != "true" || labels[remoteMetadataTenantID] != strconv.FormatUint(key.TenantID, 10) ||
		labels[remoteMetadataSessionID] != key.SessionID || labels[remoteMetadataConfigID] != binding.ConfigID ||
		labels[remoteMetadataProvider] != string(SandboxTypeDocker) || labels[remoteMetadataBindingVersion] != strconv.Itoa(SessionSandboxBindingVersion) ||
		labels[remoteMetadataIMPolicy] != policy {
		return deny("ownership labels")
	}
	h := c.HostConfig
	if string(h.NetworkMode) != "none" || h.Privileged || string(h.PidMode) == "host" || string(h.IpcMode) == "host" || string(h.UTSMode) == "host" ||
		len(h.Binds) != 0 || len(h.Mounts) != 0 || len(h.VolumesFrom) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 ||
		h.PublishAllPorts || len(h.PortBindings) != 0 {
		return deny("network, host namespace, devices or mounts")
	}
	if !h.ReadonlyRootfs || len(h.Tmpfs) != 2 || h.Tmpfs[SessionWorkspaceRoot] != imWorkspaceTmpfs || h.Tmpfs["/tmp"] != imTmpTmpfs || h.ShmSize != 64<<20 {
		return deny("read-only root or bounded temporary filesystems")
	}
	for _, m := range c.Mounts {
		if string(m.Type) != "tmpfs" || m.Source != "" || (m.Destination != SessionWorkspaceRoot && m.Destination != "/tmp") {
			return deny("unexpected actual mount")
		}
	}
	if c.NetworkSettings != nil {
		for name := range c.NetworkSettings.Networks {
			if name != "none" {
				return deny("additional network")
			}
		}
	}
	if h.Memory <= 0 || h.Memory > IMMaxMemoryBytes || h.MemorySwap != h.Memory || h.NanoCPUs <= 0 || h.NanoCPUs > int64(IMMaxCPU*1e9) ||
		h.PidsLimit == nil || *h.PidsLimit <= 0 || *h.PidsLimit > IMMaxPIDs {
		return deny("resource limits")
	}
	contains := func(items []string, value string) bool {
		for _, item := range items {
			if strings.EqualFold(item, value) {
				return true
			}
		}
		return false
	}
	if !contains(h.CapDrop, "ALL") || !contains(h.SecurityOpt, "no-new-privileges") {
		return deny("capabilities or security options")
	}
	if len(h.CapAdd) != 0 {
		return deny("added capabilities")
	}
	// Only the audited image environment and the two non-secret workspace
	// defaults may be present. Tenant or system-user env must not enter here.
	expected := map[string]string{}
	for _, entry := range img.Config.Env {
		k, v, found := strings.Cut(entry, "=")
		if !found || k == "" {
			return deny("invalid image environment")
		}
		expected[k] = v
	}
	expected[skillOutputEnvVar] = SessionOutputRoot
	expected[sessionInputEnvVar] = SessionInputRoot
	seen := map[string]bool{}
	for _, entry := range c.Config.Env {
		k, v, found := strings.Cut(entry, "=")
		if !found || seen[k] || expected[k] != v {
			return deny("unexpected environment")
		}
		if _, ok := expected[k]; !ok {
			return deny("unexpected environment")
		}
		seen[k] = true
	}
	for k := range expected {
		if !seen[k] {
			return deny("missing image environment")
		}
	}
	return nil
}

// ExecIMShell runs only on the already-inspected container. A hard per-stream
// output limit is set before Docker's buffer and before the live callback.
func (m *SessionBoundManager) ExecIMShell(ctx context.Context, sessionID, sandboxID, command, workDir string, timeout time.Duration, env map[string]string) (*ExecuteResult, error) {
	expected, err := expectedIMSandbox(ctx, sessionID)
	if err != nil || expected != sandboxID {
		return nil, errors.New("Octo sandbox: binding changed")
	}
	// Even an internal caller holding the binding context must recheck the
	// native principal and actual container before starting another command.
	inspected, err := m.EnsureIMSession(ctx, sessionID, false)
	if err != nil || inspected != sandboxID {
		return nil, errors.New("Octo sandbox: authorization or inspected binding changed")
	}
	key, err := m.sessionKey(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	handle, found, err := m.lookupSessionHandleForKey(ctx, key, sandboxID)
	if err != nil || !found {
		return nil, errors.New("Octo sandbox: inspected binding unavailable")
	}
	workDir, err = cleanSessionWorkDir(workDir, false)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > IMMaxCommandTime {
		timeout = IMMaxCommandTime
	}
	request := RemoteExecRequest{Command: command, Shell: true, WorkDir: workDir, Timeout: timeout, Env: env, User: DefaultSandboxExecUser}
	if err = setIMOutputLimit(&request, IMMaxOutputBytes/2); err != nil {
		return nil, err
	}
	// Docker exec cannot be killed via its API. Bound the complete operation
	// from outside the container and stop this exact container on abnormal
	// completion, including a modified in-container timeout binary or stream
	// overflow. Retain the pin and report any failed stop explicitly.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fail := func(cause error) (*ExecuteResult, error) {
		if stopErr := m.stopIMContainer(ctx, sandboxID); stopErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("%w: %v", ErrIMStopUnconfirmed, stopErr))
		}
		return nil, cause
	}
	// No live output may escape while a grant can be revoked mid-command.
	// The guarded caller revalidates again before returning captured output.
	request.OnOutput = nil
	if err = m.prepareSessionDirs(runCtx, handle, DefaultSandboxExecUser, SessionInputRoot, SessionOutputRoot, workDir); err != nil {
		return fail(err)
	}
	started := time.Now()
	result, err := m.client.Exec(runCtx, handle, request)
	if err != nil {
		return fail(err)
	}
	if err := runCtx.Err(); err != nil {
		return fail(err)
	}
	if result != nil && result.Killed {
		return fail(errors.New("Octo sandbox: command timed out or was killed"))
	}
	if result == nil || len(result.Stdout)+len(result.Stderr) > IMMaxOutputBytes {
		return fail(errors.New("Octo sandbox: output exceeded limit or missing result"))
	}
	// Keep the container alive for later commands and artifact collection.
	// The turn barrier stops it before the session lock is released.
	return remoteExecuteResult(result, nil, time.Since(started)), nil
}

// QuiesceIMSession stops a previously bound Octo container before a new
// turn uses or snapshots it. Policy/image differences do not authorize reads
// or execution, but must not prevent stopping an owned container. Caller must
// hold the session turn lock and fence any prior lease holder.
func (m *SessionBoundManager) QuiesceIMSession(ctx context.Context, sessionID string) (string, error) {
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || !types.HasIMKnowledgeScope(ctx) || (!grant.AllowShell && !grant.AllowSkills) || grant.SessionID != sessionID || m == nil || m.dockerClient == nil || m.lifecycle == nil || m.lifecycle.sandboxConfigID != grant.ConfigID {
		return "", types.ErrIMSandboxDenied
	}
	if err := grant.Revalidate(ctx); err != nil {
		return "", types.ErrIMSandboxDenied
	}
	key, err := m.sessionKey(ctx, sessionID)
	if err != nil || key.TenantID != grant.TenantID {
		return "", types.ErrIMSandboxDenied
	}
	var id string
	err = m.bindings.WithLifecycleLock(ctx, key, func(lockCtx context.Context) error {
		binding, err := m.lifecycle.readBinding(lockCtx, key)
		if err != nil {
			return err
		}
		if binding == nil {
			return ErrIMSandboxNotFound
		}
		if binding.Provider != SandboxTypeDocker || binding.ConfigID != grant.ConfigID {
			return types.ErrIMSandboxDenied
		}
		inspected, err := m.dockerClient.api.ContainerInspect(lockCtx, binding.SandboxID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		c := inspected.Container
		if c.ID != binding.SandboxID || c.Config == nil {
			return types.ErrIMSandboxDenied
		}
		labels := c.Config.Labels
		if labels[dockerManagedLabel] != "true" || labels[remoteMetadataTenantID] != strconv.FormatUint(key.TenantID, 10) || labels[remoteMetadataSessionID] != key.SessionID || labels[remoteMetadataConfigID] != grant.ConfigID || labels[remoteMetadataProvider] != string(SandboxTypeDocker) || labels[remoteMetadataBindingVersion] != strconv.Itoa(SessionSandboxBindingVersion) {
			return types.ErrIMSandboxDenied
		}
		if err = m.stopIMContainer(lockCtx, binding.SandboxID); err != nil {
			return fmt.Errorf("%w: %v", ErrIMStopUnconfirmed, err)
		}
		id = binding.SandboxID
		return nil
	})
	return id, err
}

// StopIMSession stops only the already-inspected, still-matching binding.
// This cleanup is permitted after authorization has been revoked; it never
// creates, substitutes or deletes a sandbox.
func (m *SessionBoundManager) StopIMSession(ctx context.Context, sessionID, sandboxID string) error {
	expected, err := expectedIMSandbox(ctx, sessionID)
	if err != nil || expected != sandboxID {
		return errors.New("Octo sandbox: stop binding mismatch")
	}
	key, err := m.sessionKey(ctx, sessionID)
	if err != nil {
		return err
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	binding, err := m.bindings.Get(cleanup, key)
	if err != nil {
		return err
	}
	if binding == nil || binding.SandboxID != sandboxID || binding.Provider != SandboxTypeDocker {
		return errors.New("Octo sandbox: stop binding changed")
	}
	return m.stopIMContainer(cleanup, sandboxID)
}

func (m *SessionBoundManager) stopIMContainer(ctx context.Context, sandboxID string) error {
	if m == nil || m.dockerClient == nil || sandboxID == "" {
		return errors.New("container identity unavailable")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	stopped := func(state *container.State) bool {
		if state == nil || state.Running || state.Paused || state.Restarting {
			return false
		}
		return state.Status == "exited" || state.Status == "dead" || state.Status == "created"
	}
	before, err := m.dockerClient.api.ContainerInspect(cleanup, sandboxID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if stopped(before.Container.State) {
		return nil
	}
	zero := 0
	_, stopErr := m.dockerClient.api.ContainerStop(cleanup, sandboxID, client.ContainerStopOptions{Timeout: &zero})
	after, inspectErr := m.dockerClient.api.ContainerInspect(cleanup, sandboxID, client.ContainerInspectOptions{})
	if inspectErr != nil {
		return errors.Join(stopErr, inspectErr)
	}
	if !stopped(after.Container.State) {
		return errors.Join(stopErr, errors.New("container stop was not confirmed"))
	}
	return nil
}

// imBoundedListProgram enumerates incrementally through directory FDs. It
// never follows symlinks, and bounds every entry (including directories), depth,
// encoded output and path length before allocating the next result.
const imBoundedListProgram = `import json,os,stat,sys
root=sys.argv[1]
directory=sys.argv[2]
if not os.path.isabs(root) or (directory!=root and not directory.startswith(root+'/')): raise ValueError('outside workspace')
flags=os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW
fd=os.open('/',flags)
try:
 for component in directory.split('/'):
  if not component: continue
  newfd=os.open(component,flags,dir_fd=fd)
  os.close(fd); fd=newfd
except FileNotFoundError:
 os.close(fd); print('[]'); sys.exit(0)
except:
 os.close(fd); raise
count=0
used=2
items=[]
def walk(parent,base,depth):
 global count,used
 with os.scandir(parent) as entries:
  for entry in entries:
   count+=1
   if count>1000: raise ValueError('entry limit')
   p=base+'/'+entry.name
   if len(p.encode('utf-8','strict'))>4096: raise ValueError('path limit')
   st=entry.stat(follow_symlinks=False)
   if stat.S_ISDIR(st.st_mode):
    if depth>=16: raise ValueError('depth limit')
    child=os.open(entry.name,flags,dir_fd=parent)
    try: walk(child,p,depth+1)
    finally: os.close(child)
   elif stat.S_ISREG(st.st_mode):
    item=json.dumps({'name':entry.name,'path':p,'size':st.st_size,'mtime':st.st_mtime},ensure_ascii=True,separators=(',',':'))
    size=len(item.encode('ascii'))+(1 if items else 0)
    if used+size>120*1024: raise ValueError('output limit')
    used+=size;items.append(item)
try:
 walk(fd,directory,0)
finally:
 os.close(fd)
print('['+','.join(items)+']')`

// ListIMSessionFiles performs bounded recursive discovery inside /workspace.
// Symlinks are never followed, including intermediate components.
func (m *SessionBoundManager) ListIMSessionFiles(ctx context.Context, sessionID, dir string) ([]RemoteDirEntry, error) {
	expected, err := expectedIMSandbox(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	clean := path.Clean(strings.TrimSpace(dir))
	if len(clean) > 4096 || (clean != SessionWorkspaceRoot && !strings.HasPrefix(clean, SessionWorkspaceRoot+"/")) {
		return nil, errors.New("Octo sandbox: directory outside workspace")
	}

	command := "python3 -c " + ShellQuote(imBoundedListProgram) + " " + ShellQuote(SessionWorkspaceRoot) + " " + ShellQuote(clean)
	result, err := m.ExecIMShell(ctx, sessionID, expected, command, SessionWorkspaceRoot, IMMaxListingTime, nil)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ExitCode != 0 || result.Killed {
		return nil, errors.New("Octo sandbox: bounded file listing failed")
	}
	var items []struct {
		Name    string  `json:"name"`
		Path    string  `json:"path"`
		Size    int64   `json:"size"`
		ModTime float64 `json:"mtime"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &items); err != nil {
		return nil, errors.New("Octo sandbox: invalid file listing")
	}
	out := make([]RemoteDirEntry, 0, len(items))
	for _, item := range items {
		if item.Path == "" || item.Name == "" || item.Size < 0 {
			return nil, errors.New("Octo sandbox: invalid file entry")
		}
		out = append(out, RemoteDirEntry{Name: item.Name, Path: item.Path, Type: RemoteEntryFile, Size: item.Size, ModTime: time.Unix(0, int64(item.ModTime*1e9))})
	}
	return out, nil
}
