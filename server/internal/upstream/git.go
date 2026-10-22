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

// BuildManifest 只读取专用裸镜像和已持久化的扫描起点。
// 观察阶段不访问本地工作树；代码是否需要吸收由后续研发任务验证。
func BuildManifest(ctx context.Context, mirror string, c Config, processed []string) (BatchManifest, error) {
	var m BatchManifest
	if strings.HasPrefix(c.UpstreamURL, "-") || strings.HasPrefix(c.UpstreamBranch, "-") {
		return m, fmt.Errorf("无效的上游配置")
	}
	if c.BootstrapBaseSHA != "" && !fullSHA.MatchString(c.BootstrapBaseSHA) {
		return m, fmt.Errorf("扫描基线不可用：请先配置有效的基线提交 SHA")
	}
	var err error
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
		// 首次扫描从上游自身的根提交建立起点，包含根提交，不依赖本地工作树。
		roots, rootErr := gitOutput(ctx, mirror, "rev-list", "--max-parents=0", m.HeadSHA)
		if rootErr != nil {
			return m, rootErr
		}
		allRoots := strings.Fields(roots)
		if len(allRoots) == 0 {
			return m, fmt.Errorf("上游没有可用的扫描起点")
		}
		m.BaseSHA = allRoots[0]
	}
	if _, err = gitOutput(ctx, mirror, "merge-base", "--is-ancestor", m.BaseSHA, m.HeadSHA); err != nil {
		return m, fmt.Errorf("原扫描起点已不属于当前上游历史")
	}
	args := []string{"rev-list", "--reverse", "--topo-order", m.HeadSHA}
	if c.BootstrapBaseSHA != "" {
		lineage, parentErr := gitOutput(ctx, mirror, "rev-list", "--parents", "-n", "1", m.BaseSHA)
		if parentErr != nil {
			return m, parentErr
		}
		// 自动建立的根起点也要由台账确认，失败重试不能跳过尚未回传的根提交。
		if len(strings.Fields(lineage)) > 1 {
			args = append(args, "^"+m.BaseSHA)
		}
	}
	all, err := gitOutput(ctx, mirror, args...)
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
		m.Commits = append(m.Commits, c)
	}
	return m, ValidateManifest(m, c.BatchSize)
}
