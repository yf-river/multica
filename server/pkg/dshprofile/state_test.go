package dshprofile

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDshSharedAdmissionExcludesActivation(t *testing.T) {
	root := t.TempDir()
	first, err := Lock(root, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Lock(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := Lock(root, true); !errors.Is(err, ErrBusy) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("activation while executions hold locks: %v", err)
	}
	first.Close()
	second.Close()
	activation, err := Lock(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := Lock(root, false); !errors.Is(err, ErrBusy) {
		if lease != nil {
			lease.Close()
		}
		t.Fatalf("execution during activation: %v", err)
	}
	activation.Close()
}

func TestDshGenerationRoundTripAndCorruption(t *testing.T) {
	root := t.TempDir()
	if state, err := Read(root); err != nil || state != nil {
		t.Fatalf("empty state: %v %v", state, err)
	}
	gen := Generation{Profile: ProfileName(root, "v1"), DSHPath: filepath.Join(root, "dsh"), DSHVersion: "0.2.1-rc.3", BridgeVersion: "0.3.0", SHA256: strings64("a"), ActivatedAt: time.Now().UTC()}
	if err := Save(root, State{Active: gen}); err != nil {
		t.Fatal(err)
	}
	old, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	gen.Profile = ProfileName(root, "v2")
	if err := Save(root, State{Active: gen, Previous: &old.Active}); err != nil {
		t.Fatal(err)
	}
	next, err := Read(root)
	if err != nil || next.Previous.Profile != old.Active.Profile {
		t.Fatalf("previous version missing: %v %v", next, err)
	}
	if err := os.WriteFile(filepath.Join(root, "active.json"), []byte(`{"active":{"profile":"../../other"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(root); err == nil {
		t.Fatal("corrupt active generation accepted")
	}
}

func strings64(s string) string {
	var b bytes.Buffer
	for range 64 {
		b.WriteString(s)
	}
	return b.String()
}

func TestDshSupportedPrereleaseRange(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{{"0.2.1-rc.2", false}, {"0.2.1-rc.3", true}, {"0.2.1", true}, {"0.2.9", true}, {"0.3.0", false}, {"garbage", false}} {
		if got := Supports(tc.version, ">=0.2.1-rc.3 <0.3.0"); got != tc.want {
			t.Errorf("%s: %v", tc.version, got)
		}
	}
	if Supports("0.2.1", "*") {
		t.Fatal("unsupported range syntax accepted")
	}
}

func TestDshArtifactVerification(t *testing.T) {
	for _, mode := range []string{"valid", "hash", "compatibility", "traversal", "symlink", "missing"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			m := Manifest{BridgeVersion: "0.3.0", ProtocolVersion: 1, DSHRange: ">=0.2.1-rc.3 <0.3.0", Filename: "dsh-runtime-0.3.0.tgz"}
			packed := m
			if mode == "compatibility" {
				packed.BridgeVersion = "0.4.0"
			}
			meta, err := json.Marshal(packed)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			z := gzip.NewWriter(&buf)
			tr := tar.NewWriter(z)
			header := &tar.Header{Name: "package/compat.json", Size: int64(len(meta)), Mode: 0o644}
			if mode == "traversal" {
				header.Name = "package/../outside"
			}
			if mode == "symlink" {
				header.Name = "package/linked"
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "/outside"
				header.Size = 0
				meta = nil
			}
			if mode == "missing" {
				header.Name = "package/other.json"
			}
			if err = tr.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if _, err = tr.Write(meta); err != nil {
				t.Fatal(err)
			}
			if err = tr.Close(); err != nil {
				t.Fatal(err)
			}
			if err = z.Close(); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()
			sum := sha256.Sum256(data)
			m.SHA256 = hex.EncodeToString(sum[:])
			m.Bytes = int64(len(data))
			if mode == "hash" {
				m.SHA256 = strings64("0")
			}
			if err = os.WriteFile(filepath.Join(root, m.Filename), data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = VerifyArtifact(root, m)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("verification error=%v", err)
			}
		})
	}
}
