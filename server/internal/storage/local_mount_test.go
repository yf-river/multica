package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalStorage_RequiredMountRejectsUnMountedDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mountinfo is Linux-specific")
	}
	mount := t.TempDir()
	uploads := filepath.Join(mount, "uploads")
	t.Setenv("LOCAL_UPLOAD_REQUIRED_MOUNT", mount)
	t.Setenv("LOCAL_UPLOAD_DIR", uploads)
	if NewLocalStorageFromEnv() != nil {
		t.Fatal("unmounted directory accepted as upload storage")
	}
	if _, err := os.Stat(uploads); !os.IsNotExist(err) {
		t.Fatalf("created a fallback upload directory: %v", err)
	}
}
