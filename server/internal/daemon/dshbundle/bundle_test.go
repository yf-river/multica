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
