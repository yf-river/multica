//go:build agentintegration

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type codebuddySmokeRuntime struct{ Path, Home, Version, SHA256 string }

// 真实测试记录实际二进制身份，防止可变安装路径与声明版本漂移。
func codebuddyRealSmokeRuntime(t *testing.T) codebuddySmokeRuntime {
	t.Helper()
	path, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
	if path == "" || home == "" {
		t.Fatal("explicit CLI and isolated account home required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DISABLE_AUTOUPDATER", "1")
	digest := func() string {
		t.Helper()
		file, err := os.Open(resolved)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		h := sha256.New()
		if _, err := io.Copy(h, file); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	before := digest()
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	version, err := DetectVersion(ctx, NewCommand(resolved, nil))
	if err != nil || !CodebuddyResidentSupported(version) {
		t.Fatalf("unverified native CodeBuddy runtime: version=%s error=%v", version, err)
	}
	if digest() != before {
		t.Fatal("CodeBuddy changed during version detection")
	}
	t.Cleanup(func() {
		if digest() != before {
			t.Error("CodeBuddy binary changed during real acceptance")
		}
	})
	t.Logf("Actual CodeBuddy runtime: version=%s sha256=%s executable=%s", version, before, resolved)
	return codebuddySmokeRuntime{Path: resolved, Home: home, Version: version, SHA256: before}
}
