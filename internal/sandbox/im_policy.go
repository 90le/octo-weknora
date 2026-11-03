package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/moby/moby/api/types/container"
)

// IMTrustedImageEnv must identify an administrator-audited, immutable Docker
// image ID whose filesystem contains no private data, credentials, or skills.
// A digest proves identity, not the safety of image contents; operators must
// inspect the image independently before configuring it.
const IMTrustedImageEnv = "OCTO_IM_SANDBOX_CLEAN_IMAGE_ID"

const (
	IMMaxCPU               = 1.0
	IMMaxMemoryBytes int64 = 1024 * 1024 * 1024
	IMMaxPIDs        int64 = 128
	IMMaxCommandTime       = 120 * time.Second
	IMMaxListingTime       = 10 * time.Second
	IMMaxOutputBytes       = 256 * 1024
)

// ErrIMDiskLimitUnavailable blocks Octo execution until a deployment supplies
// a verifier for a durable, per-session hard write quota. CPU/memory limits
// alone do not bound the Docker overlay filesystem.
var ErrIMDiskLimitUnavailable = errors.New("Octo sandbox: verified persistent per-session disk quota is unavailable")

// IMSandboxDiskVerifier is a trusted deployment integration, not a user-set
// flag. It must prove a hard write quota for the actual container and its
// persistent workspace, including data surviving stop/restart, and reject
// missing, shared, unverifiable or exhausted backing storage. No production
// implementation is wired by this branch; absence fails closed.
type IMSandboxDiskVerifier interface {
	VerifyIMSandboxDisk(context.Context, uint64, string, string, container.InspectResponse) error
}

var imImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// restrictIMSandboxConfig isolates Octo from the selected backend's shared
// snapshot, environment and network. Non-IM and file-only contexts are not
// changed. An explicitly requested execution capability fails visibly if a
// dedicated, audited base image is not configured.
func restrictIMSandboxConfig(ctx context.Context, configID string, tenant *types.TenantSandboxConfig, effective *Config) error {
	if !types.HasIMKnowledgeScope(ctx) {
		return nil
	}
	grant, ok := types.IMSandboxGrantFromContext(ctx)
	if !ok || (!grant.AllowShell && !grant.AllowSkills) || grant.ConfigID != configID {
		return fmt.Errorf("Octo sandbox: no execution authorization")
	}
	if tenant == nil || tenant.SandboxType != string(SandboxTypeDocker) || tenant.Docker == nil || effective == nil || effective.Type != SandboxTypeDocker {
		return fmt.Errorf("Octo sandbox: isolated Docker backend required")
	}
	if tenant.VolumeMount != nil {
		return fmt.Errorf("Octo sandbox: shared volume configuration is not permitted")
	}
	image := strings.TrimSpace(os.Getenv(IMTrustedImageEnv))
	if !imImageID.MatchString(image) {
		return fmt.Errorf("Octo sandbox: %s must name an audited clean image by sha256 image ID", IMTrustedImageEnv)
	}
	effective.DockerImage = image
	effective.TemplateFromSnapshot = false
	effective.EnvVars = nil
	effective.DesktopEnabled = false
	effective.DockerNetworkMode = "none"
	disabled := false
	effective.Network = RemoteNetworkPolicy{AllowInternetAccess: &disabled, AllowPublicTraffic: &disabled}
	if effective.DockerCPULimit <= 0 || effective.DockerCPULimit > IMMaxCPU {
		effective.DockerCPULimit = IMMaxCPU
	}
	if effective.DockerMemoryBytes <= 0 || effective.DockerMemoryBytes > IMMaxMemoryBytes {
		effective.DockerMemoryBytes = IMMaxMemoryBytes
	}
	if effective.DockerPidsLimit <= 0 || effective.DockerPidsLimit > IMMaxPIDs {
		effective.DockerPidsLimit = IMMaxPIDs
	}
	if effective.DefaultTimeout <= 0 || effective.DefaultTimeout > IMMaxCommandTime {
		effective.DefaultTimeout = IMMaxCommandTime
	}
	return nil
}

// WithIMSkillStaging marks writes made by the trusted approved-bundle loader.
// It does not confer permission and cannot be populated by tool arguments.
type imSkillStagingKey struct{}

func WithIMSkillStaging(ctx context.Context) context.Context {
	return context.WithValue(ctx, imSkillStagingKey{}, true)
}
func IsIMSkillStaging(ctx context.Context) bool {
	ok, _ := ctx.Value(imSkillStagingKey{}).(bool)
	return ok
}
