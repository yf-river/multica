// 一组与 git 仓库打交道的辅助函数：探测仓库、拉取远程、创建/删除
// worktree、往 .git/info/exclude 里加忽略规则，以及从 URL 提炼短名、
// 由任务 id 派生路径/分支名片段等。

package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// detectGitRepo 判断 dir 是否位于某个 git 仓库内（普通仓库或裸仓库均可）。
// 命中时返回该仓库的根路径和 true。
func detectGitRepo(dir string) (string, bool) {
	// 先试普通仓库：rev-parse --show-toplevel 会向上找 .git 并输出工作树根。
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")

	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), true
	}

	// 再试裸仓库：当 -C 正指向裸仓库时，--is-bare-repository 会输出 true，
	// 且此时仓库根就是 dir 本身（git-dir 为 "."）。
	cmd = exec.Command("git", "-C", dir, "rev-parse", "--is-bare-repository")

	if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
		return dir, true
	}

	return "", false
}

// fetchOrigin 执行 `git fetch origin`，确保本地仓库持有最新的远程引用。
func fetchOrigin(gitRoot string) error {
	cmd := exec.Command("git", "-C", gitRoot, "fetch", "origin")

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch origin: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// getRemoteDefaultBranch 返回远程默认分支对应的 "origin/<分支名>"。
// 依次回退：origin/HEAD 的 symbolic-ref → origin/main → origin/master → "HEAD"。
func getRemoteDefaultBranch(gitRoot string) string {
	// 先试 origin/HEAD（由 `git clone` 或 `git remote set-head` 维护）。
	cmd := exec.Command("git", "-C", gitRoot, "symbolic-ref", "refs/remotes/origin/HEAD")

	if out, err := cmd.Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		// ref 形如 "refs/remotes/origin/main"，去掉前缀还原成 "origin/main"。
		if strings.HasPrefix(ref, "refs/remotes/") {
			return strings.TrimPrefix(ref, "refs/remotes/")
		}
		return ref
	}

	// 回退一：origin/main 是否存在。
	cmd = exec.Command("git", "-C", gitRoot, "rev-parse", "--verify", "origin/main")

	if err := cmd.Run(); err == nil {
		return "origin/main"
	}

	// 回退二：origin/master 是否存在。
	cmd = exec.Command("git", "-C", gitRoot, "rev-parse", "--verify", "origin/master")

	if err := cmd.Run(); err == nil {
		return "origin/master"
	}

	return "HEAD"
}

// setupGitWorktree 在 worktreePath 处为 gitRoot 新建一个 worktree 和同名新分支，
// 以 baseRef 为起点。若分支名撞车则追加时间戳重试一次。
func setupGitWorktree(gitRoot, worktreePath, branchName, baseRef string) error {
	// 删掉调用方预先创建的占位目录——`git worktree add` 要求目标目录由它自己创建。
	if err := os.Remove(worktreePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove placeholder workdir: %w", err)
	}

	err := runGitWorktreeAdd(gitRoot, worktreePath, branchName, baseRef)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		// 分支名冲突：追加时间戳后重试一次。
		branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
		err = runGitWorktreeAdd(gitRoot, worktreePath, branchName, baseRef)
	}
	return err
}

// runGitWorktreeAdd 真正执行 `git worktree add -b <分支> <路径> <基准>`。
func runGitWorktreeAdd(gitRoot, worktreePath, branchName, baseRef string) error {
	cmd := exec.Command("git", "-C", gitRoot, "worktree", "add", "-b", branchName, worktreePath, baseRef)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// removeGitWorktree 删除一个 worktree 及其分支。尽力而为：失败只记日志不上报。
func removeGitWorktree(gitRoot, worktreePath, branchName string, logger *slog.Logger) {
	// 删除 worktree（--force：即使有改动也强删，因为这是受管/可再生的环境）。
	cmd := exec.Command("git", "-C", gitRoot, "worktree", "remove", "--force", worktreePath)

	if out, err := cmd.CombinedOutput(); err != nil {
		logger.Warn("execenv: git worktree remove failed", "output", strings.TrimSpace(string(out)), "error", err)
	}

	// 顺带删分支（尽力而为）。
	if branchName != "" {
		cmd = exec.Command("git", "-C", gitRoot, "branch", "-D", branchName)

		if out, err := cmd.CombinedOutput(); err != nil {
			logger.Warn("execenv: git branch delete failed", "branch", branchName, "output", strings.TrimSpace(string(out)), "error", err)
		}
	}
}

// excludeFromGit 把一个忽略模式追加到 worktree 的 .git/info/exclude 文件，
// 使该路径在本 worktree 内被 git 忽略但不改动仓库里的 .gitignore。
func excludeFromGit(worktreePath, pattern string) error {
	// 先解析该 worktree 真正的 git 目录（可能是相对路径）。
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-dir")

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("resolve git dir: %w", err)
	}

	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}

	excludePath := filepath.Join(gitDir, "info", "exclude")

	// 确保 info 目录存在。
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create info dir: %w", err)
	}

	// 若模式已存在则跳过，避免重复追加。
	existing, _ := os.ReadFile(excludePath)
	if strings.Contains(string(existing), pattern) {
		return nil
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open exclude file: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "\n%s\n", pattern); err != nil {
		return fmt.Errorf("write exclude pattern: %w", err)
	}
	return nil
}

// repoNameFromURL 从 git 远程 URL 中提取一个简短的目录名。
// 例如 "https://github.com/org/my-repo.git" → "my-repo"。
func repoNameFromURL(url string) string {
	// 去掉尾部斜杠与 .git 后缀。
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, ".git")

	// 取最后一个路径段。
	if i := strings.LastIndex(url, "/"); i >= 0 {
		url = url[i+1:]
	}
	// 兼容 SSH 风格 "host:org/repo"：先按冒号切，再按斜杠取最后段。
	if i := strings.LastIndex(url, ":"); i >= 0 {
		url = url[i+1:]
		if j := strings.LastIndex(url, "/"); j >= 0 {
			url = url[j+1:]
		}
	}

	name := strings.TrimSpace(url)
	if name == "" {
		return "repo"
	}
	return name
}

// taskKeyLen 是任务 id 在路径/分支名中用于标识任务的十六进制字符数。
// 这里的每个字符都被"花了两遍"：环境根目录前缀本身就占用了 agent 整个
// 工作目录的路径预算，而分支名又会作为 .git/refs/heads/ 下的一个路径嵌套
// 在该工作目录里；Windows 仍强制 MAX_PATH（260）。完整的 32 位 id 在深层
// 检出时会超限，所以这个片段必须保持短，靠熵而非长度来保证唯一。
const taskKeyLen = 12

// taskKey 返回用于在路径或分支名中标识任务的片段：任务 id 去掉横线后
// 的"最后" taskKeyLen 个十六进制字符。
//
// 取哪一端比取多少位更重要。任务 id 是 UUIDv7——前 48 位是毫秒时间戳，
// 后面才是随机位。开头 8 个十六进制字符对应时间戳的高 32 位，它们每
// 2^16 毫秒（约 65.5 秒）才推进一次：若从头截取，落在同一时间窗内启动的
// 所有任务会得到完全相同的片段，从而共享同一个环境根目录。那不是罕见的
// 哈希碰撞，而是常见情况，它曾导致 Prepare 的"删除已有环境"步骤误删了
// 并发运行中任务的工作目录（#7326）。
//
// 尾部取自 id 的随机字段，12 个字符携带 48 位随机熵。此外 Prepare 也拒绝
// 删除其他任务拥有的环境根，因此即使出现极不可能的撞车，也会"关闭即失败"
// （fail closed），而不是毁掉别人的工作。
//
// 日志展示请用 shortID；涉及唯一性判定的场合绝不要用 shortID。
func taskKey(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > taskKeyLen {
		return s[len(s)-taskKeyLen:]
	}
	return s
}

// shortID 返回 UUID 去掉横线后的前 8 个字符。仅用于展示和日志——
// 需要唯一性的场景一律用 taskKey，见其注释说明的原因。
func shortID(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName 把人类可读的字符串改造成可用于 git 分支名的安全形式：
// 转小写、非字母数字压成 "-"、去首尾横线、截断到 30 字符，空则兜底 "agent"。
func sanitizeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 30 {
		s = s[:30]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "agent"
	}
	return s
}
