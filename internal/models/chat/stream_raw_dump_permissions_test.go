package chat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStreamRawDumpUsesPrivateDirectoryAndFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permission bits are not reliable on Windows")
	}
	dir := filepath.Join(t.TempDir(), "stream-dump")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEKNORA_LLM_STREAM_RAW_DUMP_DIR", dir)
	dumper := newStreamPacketDumper("test-model", map[string]string{"prompt": "private request"})
	if dumper == nil {
		t.Fatal("explicit stream dump setting should remain enabled")
	}
	defer dumper.Close()
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("stream dump directory mode = %v, err = %v; want 0700", safeFileMode(info), err)
	}
	info, err = os.Stat(dumper.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stream dump file mode = %v, err = %v; want 0600", safeFileMode(info), err)
	}
}

func safeFileMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
