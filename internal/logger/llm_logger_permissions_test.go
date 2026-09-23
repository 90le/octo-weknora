package logger

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLLMDebugLogUsesPrivateDirectoryAndFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permission bits are not reliable on Windows")
	}
	dir := filepath.Join(t.TempDir(), "llm_debug")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	llmDebug.mu.Lock()
	previousEnabled, previousDir := llmDebug.enabled, llmDebug.dir
	llmDebug.mu.Unlock()
	t.Cleanup(func() {
		llmDebug.mu.Lock()
		llmDebug.enabled, llmDebug.dir = previousEnabled, previousDir
		llmDebug.mu.Unlock()
	})
	t.Setenv("LLM_DEBUG_LOG", dir)
	configureLLMDebugLog()
	if !LLMDebugEnabled() {
		t.Fatal("explicit debug setting should remain enabled")
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("debug directory mode = %v, err = %v; want 0700", infoMode(info), err)
	}

	ctx := WithRequestID(context.Background(), "private-mode-test")
	record := &LLMCallRecord{CallType: "Chat", Model: "test-model"}
	LLMDebugLog(ctx, record)
	path := filepath.Join(dir, "private-mode-test.log")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("debug file mode = %v, err = %v; want 0600", infoMode(info), err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	LLMDebugLog(ctx, record)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("existing debug file mode = %v, err = %v; want 0600", infoMode(info), err)
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
