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
  printf '%s\n' "$*" >> '`+filepath.Join(root, "install-args")+`'
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

// A managed profile that only installs the runtime bridge leaves the provider
// invisible, so a DSH agent ends up with a runtime that has no model. The
// plugin tarball ships beside the bridge for exactly that reason; this is the
// one place that contract is asserted end to end against the install command.
func TestDshManagedSupplyInstallsTheBundledProviderPlugin(t *testing.T) {
	artifacts, m := managedArtifactFixture(t)
	if err := os.WriteFile(filepath.Join(artifacts, "dsh-codebuddy-sdk-0.1.6.tgz"), []byte("plugin"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := managedDshFixture(t, "success")
	d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := d.supplyManagedDsh(context.Background(), root, artifacts, path, "0.2.1-rc.3", m); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(filepath.Dir(path), "install-args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "dsh-codebuddy-sdk-0.1.6.tgz") {
		t.Fatalf("provider plugin was not installed into the managed profile: %s", args)
	}
}

// Two versions of the same provider left in the artifact directory make the
// profile's effective plugin a matter of filesystem order, so an upgrade could
// report success while the old plugin stays installed. The supply must pick the
// higher version rather than whichever Glob happened to yield first.
func TestDshManagedSupplyInstallsOnlyTheNewestPluginVersion(t *testing.T) {
	artifacts, m := managedArtifactFixture(t)
	for _, name := range []string{"dsh-codebuddy-sdk-0.1.5.tgz", "dsh-codebuddy-sdk-0.1.6.tgz", "dsh-codebuddy-sdk-0.1.10.tgz"} {
		if err := os.WriteFile(filepath.Join(artifacts, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	path := managedDshFixture(t, "success")
	d := &Daemon{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := d.supplyManagedDsh(context.Background(), root, artifacts, path, "0.2.1-rc.3", m); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(filepath.Dir(path), "install-args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "dsh-codebuddy-sdk-0.1.10.tgz") {
		t.Fatalf("newest plugin version was not installed: %s", args)
	}
	for _, stale := range []string{"dsh-codebuddy-sdk-0.1.5.tgz", "dsh-codebuddy-sdk-0.1.6.tgz"} {
		if strings.Contains(string(args), stale) {
			t.Fatalf("stale plugin version %s was installed alongside the newest", stale)
		}
	}
}

func TestNewestPluginVersions(t *testing.T) {
	t.Run("picks the semver maximum, not the lexical maximum", func(t *testing.T) {
		directory := t.TempDir()
		for _, name := range []string{"dsh-codebuddy-sdk-0.1.9.tgz", "dsh-codebuddy-sdk-0.1.10.tgz"} {
			if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := newestPluginVersions(directory)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || filepath.Base(got[0]) != "dsh-codebuddy-sdk-0.1.10.tgz" {
			t.Fatalf("newest = %v", got)
		}
	})

	t.Run("a tarball without a parseable version is a delivery error", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "dsh-codebuddy-sdk-latest.tgz"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newestPluginVersions(directory); err == nil {
			t.Fatal("a plugin whose filename carries no version was accepted")
		}
	})

	t.Run("nothing bundled is not an error", func(t *testing.T) {
		got, err := newestPluginVersions(t.TempDir())
		if err != nil || len(got) != 0 {
			t.Fatalf("empty directory = %v, %v", got, err)
		}
	})
}

// The supply identity exists so a changed artifact reinstalls the profile. It
// must therefore change when the plugin changes, or an upgrade to a new
// provider version would be silently skipped as "already supplied".
func TestPluginInventoryTracksThePluginBytes(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, "dsh-codebuddy-sdk-0.1.6.tgz")
	if err := os.WriteFile(name, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := pluginInventory(directory)
	if err != nil || first == "" {
		t.Fatalf("inventory = %q, %v", first, err)
	}
	if err = os.WriteFile(name, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := pluginInventory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("replacing the plugin tarball did not change the supply inventory")
	}
}
