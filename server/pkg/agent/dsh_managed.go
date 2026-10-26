package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

// 供给器和执行器共用同一文件锁；一次执行固定版本直到进程收尾。
func acquireDshLaunch(ctx context.Context, path string, env map[string]string) (string, string, func(), error) {
	value := func(key string) string {
		if v, ok := env[key]; ok {
			return v
		}
		return os.Getenv(key)
	}
	profile := value("MULTICA_DSH_PROFILE")
	if profile == "" {
		profile = dshProfile
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(profile) {
		return "", "", nil, fmt.Errorf("invalid DSH profile name")
	}
	root := value(dshprofile.StateEnv)
	if value("MULTICA_DSH_MANAGED_PROFILE") == "0" {
		root = ""
	}
	if root == "" {
		return path, profile, func() {}, nil
	}
	for {
		lease, err := dshprofile.Lock(root, false)
		if err == dshprofile.ErrBusy {
			select {
			case <-ctx.Done():
				return "", "", nil, ctx.Err()
			case <-time.After(25 * time.Millisecond):
				continue
			}
		}
		if err != nil {
			return "", "", nil, err
		}
		state, err := dshprofile.Read(root)
		if err != nil || state == nil {
			_ = lease.Close()
			return "", "", nil, fmt.Errorf("managed DSH profile is not ready: %v", err)
		}
		return state.Active.DSHPath, state.Active.Profile, func() { _ = lease.Close() }, nil
	}
}
