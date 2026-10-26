package daemon

import (
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/dshbundle"
	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

// Run 在并发循环启动前配置一次；显式的运维目录与停用开关优先。
func configureEmbeddedDsh(profile string) error {
	if os.Getenv("MULTICA_DSH_MANAGED_PROFILE") == "0" {
		return nil
	}
	root, err := cli.ProfileDir(profile)
	if err != nil {
		return err
	}
	if os.Getenv(dshprofile.ArtifactEnv) == "" {
		artifacts, err := dshbundle.Extract(filepath.Join(root, "dsh-artifacts"))
		if err != nil {
			return err
		}
		if err = os.Setenv(dshprofile.ArtifactEnv, artifacts); err != nil {
			return err
		}
	}
	if os.Getenv(dshprofile.StateEnv) == "" {
		return os.Setenv(dshprofile.StateEnv, filepath.Join(root, "dsh-runtime"))
	}
	return nil
}
