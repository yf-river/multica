package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

func TestDshManagedLaunchPinsGenerationUntilRelease(t *testing.T) {
	root := t.TempDir()
	g := dshprofile.Generation{Profile: dshprofile.ProfileName(root, "one"), DSHPath: filepath.Join(root, "pinned-dsh"), DSHVersion: "0.2.1-rc.3", BridgeVersion: "0.3.0", SHA256: strings.Repeat("a", 64)}
	if err := dshprofile.Save(root, dshprofile.State{Active: g}); err != nil {
		t.Fatal(err)
	}
	path, profile, release, err := acquireDshLaunch(context.Background(), "/other/dsh", map[string]string{dshprofile.StateEnv: root})
	if err != nil {
		t.Fatal(err)
	}
	if path != g.DSHPath || profile != g.Profile {
		t.Fatalf("unpinned launch %q %q", path, profile)
	}
	if lock, err := dshprofile.Lock(root, true); err != dshprofile.ErrBusy {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("activation crossed an active execution: %v", err)
	}
	release()
	lock, err := dshprofile.Lock(root, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, _, err = acquireDshLaunch(ctx, "dsh", map[string]string{dshprofile.StateEnv: root})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked execution did not cancel: %v", err)
	}
	lock.Close()
}

func TestDshManagedLaunchNeverFallsBackFromMissingState(t *testing.T) {
	_, _, _, err := acquireDshLaunch(context.Background(), "dsh", map[string]string{dshprofile.StateEnv: t.TempDir()})
	if err == nil {
		t.Fatal("missing managed state silently used legacy profile")
	}
}
