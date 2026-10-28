package dshbundle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

func TestEmbeddedBridgeExtractAndRejectTampering(t *testing.T) {
	base := t.TempDir()
	path, err := Extract(base)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := dshprofile.ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BridgeVersion != "0.3.3" || manifest.ProtocolVersion != 1 {
		t.Fatalf("wrong embedded identity: %#v", manifest)
	}
	if repeated, err := Extract(base); err != nil || repeated != path {
		t.Fatalf("idempotent extraction: %q %v", repeated, err)
	}
	if err = os.WriteFile(filepath.Join(path, manifest.Filename), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Extract(base); err == nil {
		t.Fatal("tampered embedded installation accepted")
	}
}

// A provider plugin ships in the bundle next to the bridge so a managed profile
// can install it offline. Extraction must land it beside the runtime in the
// artifact directory, which is the only place the supply step looks.
func TestExtractionInstallsTheBundledProviderPlugin(t *testing.T) {
	base := t.TempDir()
	path, err := Extract(base)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(path, "dsh-codebuddy-sdk-*.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("bundled plugins = %v, want exactly one version", entries)
	}
	if filepath.Base(entries[0]) != "dsh-codebuddy-sdk-0.1.6.tgz" {
		t.Fatalf("bundled plugin = %s, want 0.1.6", entries[0])
	}
}

// Upgrading the plugin leaves the previous release's tarball in the hash-pinned
// artifact directory. Supply installs by glob, so keeping the old file would
// make the installed version depend on filesystem order; re-extraction must
// remove it.
func TestExtractionRemovesSupersededPluginVersions(t *testing.T) {
	base := t.TempDir()
	path, err := Extract(base)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(path, "dsh-codebuddy-sdk-0.1.5.tgz")
	if err = os.WriteFile(stale, []byte("old release"), 0o644); err != nil {
		t.Fatal(err)
	}
	repeated, err := Extract(base)
	if err != nil || repeated != path {
		t.Fatalf("idempotent extraction: %q %v", repeated, err)
	}
	if _, err = os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("superseded plugin survived re-extraction: %v", err)
	}
	entries, err := filepath.Glob(filepath.Join(path, "dsh-codebuddy-sdk-*.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || filepath.Base(entries[0]) != "dsh-codebuddy-sdk-0.1.6.tgz" {
		t.Fatalf("plugins after re-extraction = %v", entries)
	}
}
