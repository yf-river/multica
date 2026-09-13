// 定义"任务环境里哪些子目录由 daemon 管理、可随时重新生成"，
// 供回收（GC/清理）逻辑决定哪些内容可以被安全删除。

package execenv

import "path/filepath"

const (
	codexHomeDirName       = "codex-home"
	codexSandboxBinDirName = ".sandbox-bin"
)

// ManagedReclaimableArtifactSubpaths 返回任务环境根目录下"由 daemon 拥有、
// 可再生的"目录（相对路径）。调用方必须按完整相对路径精确匹配，不能只比
// 叶子名：仓库本身也可能合法地含有一个同名的目录。
func ManagedReclaimableArtifactSubpaths() []string {
	return []string{filepath.Join(codexHomeDirName, codexSandboxBinDirName)}
}
