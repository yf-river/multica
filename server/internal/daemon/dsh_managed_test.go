package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

func managedArtifactFixture(t *testing.T) (string, dshprofile.Manifest) {
	t.Helper()
	root := t.TempDir()
	m := dshprofile.Manifest{BridgeVersion: "0.3.0", ProtocolVersion: 1, DSHRange: ">=0.2.1-rc.3 <0.3.0", Filename: "dsh-runtime-0.3.0.tgz"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	tr := tar.NewWriter(z)
	if err = tr.WriteHeader(&tar.Header{Name: "package/compat.json", Mode: 0o644, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tr.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	m.SHA256 = hex.EncodeToString(sum[:])
	m.Bytes = int64(buf.Len())
	if err = os.WriteFile(filepath.Join(root, m.Filename), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, m
}

func managedDshFixture(t *testing.T, mode string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix process fixture; admission lock tests run on Windows")
	}
	root := t.TempDir()
	path := filepath.Join(root, "dsh")
	if err := os.WriteFile(path, []byte(`#!/bin/sh
set -eu
if [ "$1" = "plugin" ]; then
  printf 'install\n' >> '`+filepath.Join(root, "installs")+`'
  `+map[string]string{"install-failure": "exit 9", "success": "exit 0", "post-probe-failure": "exit 0"}[mode]+`
fi
if [ "$3" = "--probe" ]; then
  count=0
  if [ -f '`+filepath.Join(root, "probe-count")+`' ]; then count=$(cat '`+filepath.Join(root, "probe-count")+`'); fi
  count=$((count + 1))
  printf '%s' "$count" > '`+filepath.Join(root, "probe-count")+`'
  if [ '`+mode+`' = 'post-probe-failure' ] && [ "$count" -gt 2 ]; then exit 8; fi
  printf '%s\n' '{"v":1,"type":"probe","runtime":"dsh","protocol_version":1,"plugin_version":"0.3.0"}'
  exit 0
fi
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","protocol_version":1}'
IFS= read -r command
case "$command" in
  *'"command":"/plan'* ) ;;
  * ) printf '%s\n' '{"v":1,"type":"context","session_id":"canary","duration_ms":1,"input_tokens":1,"output_tokens":1}' ;;
esac
printf '%s\n' '{"v":1,"type":"result","status":"completed","session_id":"canary","output":"MULTICA_DSH_CANARY_OK"}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDshManagedSupplyActivatesOnlyAfterIdleAndKeepsPrevious(t *testing.T) {
	artifacts, m := managedArtifactFixture(t)
	root := t.TempDir()
	path := managedDshFixture(t, "success")
	d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	old := dshprofile.Generation{Profile: dshprofile.ProfileName(root, "old"), DSHPath: path, DSHVersion: "0.2.1-rc.3", BridgeVersion: "0.2.0", SHA256: strings.Repeat("a", 64)}
	if err := dshprofile.Save(root, dshprofile.State{Active: old}); err != nil {
		t.Fatal(err)
	}
	d.activeTasks.Store(1)
	err := d.supplyManagedDsh(context.Background(), root, artifacts, path, "0.2.1-rc.3", m)
	if !errors.Is(err, dshprofile.ErrBusy) {
		t.Fatalf("busy task allowed activation: %v", err)
	}
	state, err := dshprofile.Read(root)
	if err != nil || state.Active.Profile != old.Profile {
		t.Fatalf("active version changed while busy: %v", err)
	}
	if readDshSupplyStatus(root).Status != "validated" {
		t.Fatal("validated staging was discarded")
	}
	d.activeTasks.Store(0)
	if err = d.supplyManagedDsh(context.Background(), root, artifacts, path, "0.2.1-rc.3", m); err != nil {
		t.Fatal(err)
	}
	state, err = dshprofile.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active.SHA256 != m.SHA256 || state.Previous == nil || state.Previous.Profile != old.Profile {
		t.Fatalf("N-1 missing: %#v", state)
	}
	installs, err := os.ReadFile(filepath.Join(filepath.Dir(path), "installs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(installs), "install") != 1 {
		t.Fatal("idle retry reinstalled a validated candidate")
	}
}

func TestDshManagedSupplyFailureQuarantinesAndRetainsActive(t *testing.T) {
	for _, mode := range []string{"install-failure", "post-probe-failure"} {
		t.Run(mode, func(t *testing.T) {
			artifacts, m := managedArtifactFixture(t)
			root := t.TempDir()
			path := managedDshFixture(t, mode)
			d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			old := dshprofile.Generation{Profile: dshprofile.ProfileName(root, "old"), DSHPath: path, DSHVersion: "0.2.1-rc.3", BridgeVersion: "0.2.0", SHA256: strings.Repeat("a", 64)}
			if err := dshprofile.Save(root, dshprofile.State{Active: old}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := d.supplyManagedDsh(context.Background(), root, artifacts, path, "0.2.1-rc.3", m); err == nil {
					t.Fatal("bad candidate succeeded")
				}
			}
			state, err := dshprofile.Read(root)
			if err != nil || state.Active.Profile != old.Profile {
				t.Fatalf("old version not restored: %v", err)
			}
			if readDshSupplyStatus(root).Status != "degraded" {
				t.Fatal("failure not persisted")
			}
			installs, err := os.ReadFile(filepath.Join(filepath.Dir(path), "installs"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(installs), "install") != 1 {
				t.Fatal("quarantined candidate retried installation")
			}
		})
	}
}
