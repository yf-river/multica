package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

func TestDshEmbeddedProfileUsesPrivateCLIState(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	t.Setenv("MULTICA_DSH_MANAGED_PROFILE", "")
	t.Setenv(dshprofile.ArtifactEnv, "")
	t.Setenv(dshprofile.StateEnv, "")
	if err := configureEmbeddedDsh("test-dsh"); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "profiles", "test-dsh")
	if os.Getenv(dshprofile.StateEnv) != filepath.Join(profile, "dsh-runtime") {
		t.Fatal("embedded bridge escaped profile state")
	}
	m, err := dshprofile.ReadManifest(os.Getenv(dshprofile.ArtifactEnv))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dshprofile.VerifyArtifact(os.Getenv(dshprofile.ArtifactEnv), m); err != nil {
		t.Fatal(err)
	}
}

func TestDshEmbeddedProfileHonorsExplicitDisable(t *testing.T) {
	t.Setenv("MULTICA_DSH_MANAGED_PROFILE", "0")
	t.Setenv(dshprofile.ArtifactEnv, "")
	t.Setenv(dshprofile.StateEnv, "")
	if err := configureEmbeddedDsh("test-dsh"); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(dshprofile.ArtifactEnv) != "" || os.Getenv(dshprofile.StateEnv) != "" {
		t.Fatal("disabled profile provision changed environment")
	}
}
