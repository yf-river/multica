//go:build linux

package execenv

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// 旧版冷进程不在常驻池记录中；仍声明来源 home 的同用户进程不能与迁移并行。
// 无所有权记录的外部进程只拒绝迁移，不按路径推断可终止它。
func ensureCodexNativeStateIdle(home string) error {
	homeInfo, err := os.Stat(home)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(owner.Uid) != os.Getuid() {
			continue
		}
		environment, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			continue
		}
		for _, field := range bytes.Split(environment, []byte{0}) {
			name, value, ok := strings.Cut(string(field), "=")
			if !ok || (name != "CODEX_HOME" && name != "CODEX_SQLITE_HOME") {
				continue
			}
			candidate, err := os.Stat(value)
			if err == nil && os.SameFile(homeInfo, candidate) {
				return errCodexNativeStateInUse
			}
		}
	}
	return nil
}
