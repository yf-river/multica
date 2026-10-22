package upstream

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func gitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// BuildManifest 只更新专用裸镜像；当前代码工作树和分支不参与写入。
// 每轮按固定起点枚举整张提交图，再减去已完成台账，避免 SHA 游标跳过旁支。
func BuildManifest(ctx context.Context, repo, mirror string, c Config, processed []string) (BatchManifest, error) {
	var m BatchManifest
	if strings.HasPrefix(c.UpstreamURL, "-") || strings.HasPrefix(c.UpstreamBranch, "-") {
		return m, fmt.Errorf("无效的上游配置")
	}
	local, err := gitOutput(ctx, repo, "rev-parse", "--verify", "--end-of-options", c.LocalRef+"^{commit}")
	if err != nil {
		return m, fmt.Errorf("本地比较基线不可用: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(mirror), 0700); err != nil {
		return m, err
	}
	if _, err = os.Stat(filepath.Join(mirror, "HEAD")); os.IsNotExist(err) {
		if err = os.MkdirAll(mirror, 0700); err != nil {
			return m, err
		}
		if _, err = gitOutput(ctx, mirror, "init", "--bare"); err != nil {
			return m, err
		}
	}
	if _, err = gitOutput(ctx, mirror, "fetch", "--no-tags", c.UpstreamURL, "+refs/heads/"+c.UpstreamBranch+":refs/upstream/head"); err != nil {
		return m, err
	}
	// local 的对象也放入隔离镜像，用于祖先关系和 patch-id 比较。
	if _, err = gitOutput(ctx, mirror, "fetch", "--no-tags", repo, local+":refs/local/head"); err != nil {
		return m, err
	}
	m.HeadSHA, err = gitOutput(ctx, mirror, "rev-parse", "refs/upstream/head")
	if err != nil {
		return m, err
	}
	if c.HeadSHA != "" {
		if _, err = gitOutput(ctx, mirror, "merge-base", "--is-ancestor", c.HeadSHA, m.HeadSHA); err != nil {
			return m, fmt.Errorf("上游历史发生改写，保留原游标并等待核查")
		}
	}
	m.BaseSHA = c.BootstrapBaseSHA
	if m.BaseSHA == "" {
		m.BaseSHA, err = gitOutput(ctx, mirror, "merge-base", m.HeadSHA, local)
		if err != nil {
			return m, fmt.Errorf("未找到共同历史，需核对本地基线: %w", err)
		}
	}
	if _, err = gitOutput(ctx, mirror, "merge-base", "--is-ancestor", m.BaseSHA, m.HeadSHA); err != nil {
		return m, fmt.Errorf("原扫描起点已不属于当前上游历史")
	}
	all, err := gitOutput(ctx, mirror, "rev-list", "--reverse", "--topo-order", m.HeadSHA, "^"+m.BaseSHA)
	if err != nil {
		return m, err
	}
	known := map[string]bool{}
	for _, sha := range processed {
		known[sha] = true
	}
	pending := []string{}
	for _, sha := range strings.Fields(all) {
		if !known[sha] {
			pending = append(pending, sha)
		}
	}
	m.HasMore = len(pending) > c.BatchSize
	if m.HasMore {
		pending = pending[:c.BatchSize]
	}
	equivalent := map[string]bool{}
	cherry, err := gitOutput(ctx, mirror, "cherry", local, m.HeadSHA, m.BaseSHA)
	if err != nil {
		return m, err
	}
	for _, line := range strings.Split(cherry, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == "-" {
			equivalent[parts[1]] = true
		}
	}
	m.Commits = []CommitRef{}
	for _, sha := range pending {
		subject, err := gitOutput(ctx, mirror, "show", "-s", "--format=%s", sha)
		if err != nil {
			return m, err
		}
		stamp, e := gitOutput(ctx, mirror, "show", "-s", "--format=%ct", sha)
		if e != nil {
			return m, e
		}
		seconds, e := strconv.ParseInt(stamp, 10, 64)
		if e != nil {
			return m, e
		}
		at := time.Unix(seconds, 0).UTC()
		c := CommitRef{SHA: sha, Subject: subject, CommittedAt: &at}
		cmd := exec.CommandContext(ctx, "git", "-C", mirror, "merge-base", "--is-ancestor", sha, local)
		if cmd.Run() == nil {
			c.Absorbed = true
			c.AbsorbedMethod = "ancestry"
		} else if equivalent[sha] {
			c.Absorbed = true
			c.AbsorbedMethod = "patch_id"
		}
		m.Commits = append(m.Commits, c)
	}
	return m, ValidateManifest(m, c.BatchSize)
}
