//go:build agentintegration

package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

func TestDshManagedRealSupplySmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("real DSH supply smoke is opt-in")
	}
	path, artifacts := os.Getenv("MULTICA_DSH_PATH"), os.Getenv("MULTICA_DSH_MANAGED_ARTIFACTS")
	if path == "" || artifacts == "" {
		t.Fatal("explicit DSH path and artifact directory are required")
	}
	m, err := dshprofile.ReadManifest(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	// 空包缓存验证交付 tarball 可离线安装；DSH 自身提供已安装的宿主依赖。
	t.Setenv("npm_config_store_dir", filepath.Join(root, "empty-pnpm-store"))
	defer func() {
		status := readDshSupplyStatus(root)
		if status.Generation.Profile == "" {
			return
		}
		home := os.Getenv("DSH_HOME")
		if home == "" {
			userHome, e := os.UserHomeDir()
			if e != nil {
				t.Error(e)
				return
			}
			home = filepath.Join(userHome, ".dsh")
		}
		if e := os.RemoveAll(filepath.Join(home, "profiles", status.Generation.Profile)); e != nil {
			t.Error(e)
		}
	}()
	d := &Daemon{logger: slog.Default()}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err = d.supplyManagedDsh(ctx, root, artifacts, path, "0.2.1-rc.3", m); err != nil {
		t.Fatal(err)
	}
	state, err := dshprofile.Read(root)
	if err != nil || state == nil {
		t.Fatalf("activation missing: %v", err)
	}
	if state.Active.SHA256 != m.SHA256 || readDshSupplyStatus(root).Status != "active" {
		t.Fatal("wrong activated artifact")
	}
	t.Logf("real staging install/probe/plain/context/plan/resume/activation passed; bridge=%s sha256=%s", m.BridgeVersion, m.SHA256)
}
