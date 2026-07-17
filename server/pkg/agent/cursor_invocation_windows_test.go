//go:build windows

package agent

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func stubPowerShell(t *testing.T, path string, ok bool) {
	t.Helper()
	prev := powerShellLookup
	powerShellLookup = func() (string, bool) { return path, ok }
	t.Cleanup(func() { powerShellLookup = prev })
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type platformInvocation func(string, []string, *slog.Logger) (string, []string, bool)

func assertWindowsLauncherContract(t *testing.T, executable string, args []string, invoke platformInvocation) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("rewrites cmd launcher through PowerShell", func(t *testing.T) {
		dir := t.TempDir()
		cmdPath := filepath.Join(dir, executable+".cmd")
		ps1Path := filepath.Join(dir, executable+".ps1")
		writeFile(t, cmdPath, "@echo off\r\n")
		writeFile(t, ps1Path, "# fake\r\n")
		fakePS := filepath.Join(dir, "powershell.exe")
		writeFile(t, fakePS, "")
		stubPowerShell(t, fakePS, true)

		gotExec, gotArgs, ok := invoke(cmdPath, args, logger)
		if !ok || gotExec != fakePS {
			t.Fatalf("rewrite = (%q, %t), want (%q, true)", gotExec, ok, fakePS)
		}
		wantArgs := append([]string{
			"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1Path,
		}, args...)
		if !reflect.DeepEqual(gotArgs, wantArgs) {
			t.Fatalf("argv = %#v, want %#v", gotArgs, wantArgs)
		}
	})

	t.Run("skips direct executable", func(t *testing.T) {
		dir := t.TempDir()
		exePath := filepath.Join(dir, executable+".exe")
		writeFile(t, exePath, "")
		writeFile(t, filepath.Join(dir, executable+".ps1"), "")
		stubPowerShell(t, filepath.Join(dir, "powershell.exe"), true)
		if _, _, ok := invoke(exePath, args, logger); ok {
			t.Fatal("direct executable was rewritten")
		}
	})

	t.Run("skips launcher without script", func(t *testing.T) {
		dir := t.TempDir()
		cmdPath := filepath.Join(dir, executable+".cmd")
		writeFile(t, cmdPath, "@echo off\r\n")
		stubPowerShell(t, filepath.Join(dir, "powershell.exe"), true)
		if _, _, ok := invoke(cmdPath, args, logger); ok {
			t.Fatal("launcher without PowerShell script was rewritten")
		}
	})

	t.Run("skips launcher without PowerShell", func(t *testing.T) {
		dir := t.TempDir()
		cmdPath := filepath.Join(dir, executable+".cmd")
		writeFile(t, cmdPath, "@echo off\r\n")
		writeFile(t, filepath.Join(dir, executable+".ps1"), "# fake\r\n")
		stubPowerShell(t, "", false)
		if _, _, ok := invoke(cmdPath, args, logger); ok {
			t.Fatal("launcher without PowerShell host was rewritten")
		}
	})
}

func TestPlatformCursorInvocation(t *testing.T) {
	assertWindowsLauncherContract(t, "cursor-agent", []string{
		"-p", "line1\nline2\nline3",
		"--output-format", "stream-json", "--yolo", "--workspace", `C:\some\workspace`,
	}, platformCursorInvocation)
}
