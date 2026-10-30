package openviking

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentDefaultsOffAndProductionRemainsEnabled(t *testing.T) {
	t.Setenv("MULTICA_OPENVIKING_ENABLED", "")
	t.Setenv("MULTICA_OPENVIKING_URL", "http://127.0.0.1:1933")
	if err := ValidateEnvironment(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_OPENVIKING_ENABLED", "true")
	t.Setenv("MULTICA_ENV_ID", "prod")
	t.Setenv("MULTICA_LOCAL_DEV", "")
	if err := ValidateEnvironment(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_LOCAL_DEV", "1")
	if err := ValidateEnvironment(); err == nil {
		t.Fatal("local development inherited production identity")
	}
}

func TestTestEnvironmentRequiresAcceptanceAndIndependentStorage(t *testing.T) {
	root := t.TempDir()
	prod := filepath.Join(root, "prod")
	data := filepath.Join(root, "test-data")
	archive := filepath.Join(root, "test-archive")
	prodArchive := filepath.Join(root, "prod-archive")
	for _, p := range []string{prod, data, archive, prodArchive} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, value any) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	meta := filepath.Join(root, "production.json")
	write(meta, map[string]any{"environment": "prod", "openviking": DeploymentIdentity{Enabled: true, URL: "http://127.0.0.1:1933", DataRoot: prod, ArchiveRoot: prodArchive, LocalStorage: true}})
	config := filepath.Join(root, "ov.conf")
	native := func(workspace string, backend map[string]any) {
		write(config, map[string]any{"server": map[string]any{"host": "127.0.0.1", "port": 19446}, "storage": map[string]any{"workspace": workspace, "agfs": backend, "vectordb": map[string]string{"backend": "local"}}})
	}
	native(data, map[string]any{"backend": "local"})
	t.Setenv("MULTICA_ENV_ID", "int")
	t.Setenv("MULTICA_OPENVIKING_ENABLED", "true")
	t.Setenv("MULTICA_OPENVIKING_URL", "http://127.0.0.1:19446")
	t.Setenv("MULTICA_OPENVIKING_CONFIG_FILE", config)
	t.Setenv("MULTICA_OPENVIKING_ARCHIVE_ROOT", archive)
	t.Setenv("MULTICA_OPENVIKING_PRODUCTION_DEPLOYMENT", meta)
	t.Setenv("MULTICA_OPENVIKING_ACCEPTANCE", "")
	if err := ValidateEnvironment(); err == nil {
		t.Fatal("ordinary test enabled native access")
	}
	t.Setenv("MULTICA_OPENVIKING_ACCEPTANCE", "true")
	for _, target := range []string{"http://localhost:1933", "http://127.0.0.1:1933", "http://192.0.2.1:1933"} {
		t.Setenv("MULTICA_OPENVIKING_URL", target)
		if err := ValidateEnvironment(); err == nil {
			t.Fatalf("production target accepted: %s", target)
		}
	}
	t.Setenv("MULTICA_OPENVIKING_URL", "http://127.0.0.1:19446")
	for _, workspace := range []string{prod, filepath.Join(prod, "child")} {
		os.MkdirAll(workspace, 0700)
		native(workspace, map[string]any{"backend": "local"})
		err := ValidateEnvironment()
		if err == nil || !strings.Contains(err.Error(), "重叠") {
			t.Fatalf("shared production data not rejected: %v", err)
		}
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(prod, link); err != nil {
		t.Fatal(err)
	}
	native(link, map[string]any{"backend": "local"})
	if err := ValidateEnvironment(); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("symlink share not rejected: %v", err)
	}
	native(data, map[string]any{"backend": "local", "root": prod})
	if err := ValidateEnvironment(); err == nil || !strings.Contains(err.Error(), "外部") {
		t.Fatalf("unverified storage override accepted: %v", err)
	}
	native(data, map[string]any{"backend": "local"})
	t.Setenv("MULTICA_OPENVIKING_ARCHIVE_ROOT", prodArchive)
	if err := ValidateEnvironment(); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("production archive accepted: %v", err)
	}
	t.Setenv("MULTICA_OPENVIKING_ARCHIVE_ROOT", archive)
	t.Setenv("MULTICA_OPENVIKING_PRODUCTION_DEPLOYMENT", filepath.Join(root, "missing"))
	if err := ValidateEnvironment(); err == nil {
		t.Fatal("unconfirmed production identity accepted")
	}
}
