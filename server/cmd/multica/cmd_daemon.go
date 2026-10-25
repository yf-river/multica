// 文件职责：实现 `multica daemon` 命令组——本地 agent runtime 守护进程的启停
// （start/stop/restart）、状态查询（status）、日志查看（logs）与工作区磁盘占用
// 统计（disk-usage）。前台 daemon 本体也由本文件（runDaemonForeground）拉起，其余
// daemon 运行时逻辑在 server/internal/daemon。配置文件、日志、PID 与健康端口都按
// profile 隔离（健康端口由 profile 名散列得到，见 healthPortForProfile）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	logger_pkg "github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/selfexec"
	"github.com/multica-ai/multica/server/internal/util"
)

// daemonCmd 是 `multica daemon` 父命令。
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Control the local agent runtime daemon",
}

// daemonStartCmd 启动 daemon（默认后台运行，--foreground 前台）。
var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the local agent runtime daemon",
	Long:  "Start the daemon process that polls for tasks and executes them using local agent CLIs (Claude, Codex).\nRuns in the background by default. Use --foreground to run in the current terminal.",
	RunE:  runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running daemon",
	RunE:  runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE:  runDaemonStatus,
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the running daemon (stop + start)",
	RunE:  runDaemonRestart,
}

var daemonLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show daemon logs",
	RunE:  runDaemonLogs,
}

var daemonDiskUsageCmd = &cobra.Command{
	Use:   "disk-usage",
	Short: "Show daemon workspace disk usage by task or workspace",
	Long: "Walks the daemon's workspaces root and reports per-task or per-workspace disk usage.\n" +
		"Default view is per-task, sorted by size descending. --by-workspace switches to a per-workspace summary;\n" +
		"--top N keeps only the largest N entries.\n\n" +
		"By default only the current profile's root is scanned. --all-profiles aggregates across every workspace\n" +
		"root — the default root plus each ~/.multica/profiles/* root — and prints a per-root breakdown with a\n" +
		"combined grand total. In that mode --top\n" +
		"applies within each root and --workspaces-root is not allowed.\n\n" +
		"Bytes are split into total and the artifact-cleanable subset (node_modules, .next, .turbo by default,\n" +
		"overridable via MULTICA_GC_ARTIFACT_PATTERNS) so the report stays in sync with what the GC reclaims.\n" +
		"A .git directory counts toward the total but never toward the artifact subset — the GC frees it only\n" +
		"when it removes the whole task directory. Symlinks are never followed.\n\n" +
		"The STATUS column shows the current status of the issue each directory belongs to, which requires\n" +
		"contacting the server. When the machine is offline, logged out, or the request fails, the column is\n" +
		"left blank and the rest of the report still prints. --by-workspace has no STATUS column and therefore\n" +
		"makes no network request unless --output json is also set. The daemon does not need to be running.",
	RunE: runDaemonDiskUsage,
}

func init() {
	f := daemonStartCmd.Flags()
	f.Bool("foreground", false, "Run in the foreground instead of background")
	f.String("daemon-id", "", "Unique daemon identifier (env: MULTICA_DAEMON_ID)")
	f.String("device-name", "", "Human-readable device name (env: MULTICA_DAEMON_DEVICE_NAME)")
	f.String("runtime-name", "", "Runtime display name (env: MULTICA_AGENT_RUNTIME_NAME)")
	f.String("workspaces-root", "", "Base directory for task workspaces (env: MULTICA_WORKSPACES_ROOT)")
	f.Duration("poll-interval", 0, "Task poll interval (env: MULTICA_DAEMON_POLL_INTERVAL)")
	f.Duration("heartbeat-interval", 0, "Heartbeat interval (env: MULTICA_DAEMON_HEARTBEAT_INTERVAL)")
	f.Duration("agent-timeout", 0, "Absolute per-task wall-clock cap; 0 = no cap, rely on the watchdogs (env: MULTICA_AGENT_TIMEOUT)")
	f.Duration("codex-semantic-inactivity-timeout", 0, "Codex semantic inactivity timeout (env: MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT)")
	f.Duration("codex-handshake-timeout", 0, "Codex app-server startup RPC timeout (env: MULTICA_CODEX_HANDSHAKE_TIMEOUT)")
	f.Int("max-concurrent-tasks", 0, "Max tasks running in parallel (env: MULTICA_DAEMON_MAX_CONCURRENT_TASKS)")
	f.Bool("no-auto-update", false, "Disable periodic CLI self-update (env: MULTICA_DAEMON_AUTO_UPDATE=false)")
	f.Duration("auto-update-interval", 0, "How often to poll GitHub for a newer release (env: MULTICA_DAEMON_AUTO_UPDATE_INTERVAL)")
	f.Bool("no-auto-reload", false, "Disable restarting when the multica binary on disk changes version (env: MULTICA_DAEMON_AUTO_RELOAD=false)")

	daemonLogsCmd.Flags().BoolP("follow", "f", false, "Follow log output")
	daemonLogsCmd.Flags().IntP("lines", "n", 50, "Number of lines to show")

	daemonStatusCmd.Flags().String("output", "table", "Output format: table or json")

	// restart 与 start 共用同一套 flag
	rf := daemonRestartCmd.Flags()
	rf.Bool("foreground", false, "Run in the foreground instead of background")
	rf.String("daemon-id", "", "Unique daemon identifier (env: MULTICA_DAEMON_ID)")
	rf.String("device-name", "", "Human-readable device name (env: MULTICA_DAEMON_DEVICE_NAME)")
	rf.String("runtime-name", "", "Runtime display name (env: MULTICA_AGENT_RUNTIME_NAME)")
	rf.String("workspaces-root", "", "Base directory for task workspaces (env: MULTICA_WORKSPACES_ROOT)")
	rf.Duration("poll-interval", 0, "Task poll interval (env: MULTICA_DAEMON_POLL_INTERVAL)")
	rf.Duration("heartbeat-interval", 0, "Heartbeat interval (env: MULTICA_DAEMON_HEARTBEAT_INTERVAL)")
	rf.Duration("agent-timeout", 0, "Absolute per-task wall-clock cap; 0 = no cap, rely on the watchdogs (env: MULTICA_AGENT_TIMEOUT)")
	rf.Duration("codex-semantic-inactivity-timeout", 0, "Codex semantic inactivity timeout (env: MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT)")
	rf.Duration("codex-handshake-timeout", 0, "Codex app-server startup RPC timeout (env: MULTICA_CODEX_HANDSHAKE_TIMEOUT)")
	rf.Int("max-concurrent-tasks", 0, "Max tasks running in parallel (env: MULTICA_DAEMON_MAX_CONCURRENT_TASKS)")
	rf.Bool("no-auto-update", false, "Disable periodic CLI self-update (env: MULTICA_DAEMON_AUTO_UPDATE=false)")
	rf.Duration("auto-update-interval", 0, "How often to poll GitHub for a newer release (env: MULTICA_DAEMON_AUTO_UPDATE_INTERVAL)")
	rf.Bool("no-auto-reload", false, "Disable restarting when the multica binary on disk changes version (env: MULTICA_DAEMON_AUTO_RELOAD=false)")

	df := daemonDiskUsageCmd.Flags()
	df.Bool("by-workspace", false, "Aggregate output by workspace instead of by task")
	df.Bool("by-task", false, "Per-task view (default; mutually exclusive with --by-workspace)")
	df.Int("top", 0, "Keep only the largest N entries (per root in --all-profiles mode)")
	df.String("output", "table", "Output format: table or json")
	df.String("workspaces-root", "", "Override the workspaces root path (default: same as the daemon)")
	df.Bool("all-profiles", false, "Scan every workspace root (default root + all ~/.multica/profiles/* roots) and report a combined total")

	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	daemonCmd.AddCommand(daemonDiskUsageCmd)
}

// daemonDirForProfile 返回给定 profile 的状态目录：空 profile → ~/.multica/，
// 命名 profile → ~/.multica/profiles/<name>/。
func daemonDirForProfile(profile string) string {
	dir, err := cli.ProfileDir(profile)
	if err != nil {
		return ""
	}
	return dir
}

// daemonPIDPathForProfile 返回该 profile 的 PID 文件路径。
func daemonPIDPathForProfile(profile string) string {
	return filepath.Join(daemonDirForProfile(profile), "daemon.pid")
}

// daemonLogPathForProfile 返回该 profile 的 daemon.log（结构化日志）路径。
func daemonLogPathForProfile(profile string) string {
	return filepath.Join(daemonDirForProfile(profile), "daemon.log")
}

// daemonStderrLogPathForProfile 是前台 daemon 原始 stdout/stderr 的落点——Go
// 运行时 panic、致命 abort、以及打进 logger 之前的输出。故意与 daemon.log 分开：
// 前台 daemon 通过轮转 writer（newDaemonLogRotator）持有 daemon.log，若让子进程还
// 经继承的 fd 也开着 daemon.log，会在 Windows 上阻塞轮转的改名。每条结构化日志
// 本就走 slog 进轮转的 daemon.log，因此这个文件只收极少见的崩溃输出，健康 daemon
// 下基本保持为空。
func daemonStderrLogPathForProfile(profile string) string {
	return filepath.Join(daemonDirForProfile(profile), "daemon.err.log")
}

// Daemon 日志轮转策略。默认值让活跃的 daemon.log 小到能直接开在编辑器里、同时
// 保留近期历史；每个都能用环境变量覆盖，运维不必重编译即可调保留策略。备份数与
// 天数哪个先到先触发清理。
const (
	defaultDaemonLogMaxSizeMB  = 20 // 活跃 daemon.log 长到该尺寸就轮转
	defaultDaemonLogMaxBackups = 5  // 保留多少个轮转文件
	defaultDaemonLogMaxAgeDays = 30 // 丢弃早于该天数的轮转文件
	// errLogMaxBytes 给 daemon.err.log（原始崩溃/panic 落点）封顶。打开文件时若超限
	// 先把它滚成一份 ".1" 备份再写，这样崩溃循环反复追加 panic 输出也不会在另一个
	// 文件上重新引入无界增长问题。
	errLogMaxBytes = 5 * 1024 * 1024
)

// newDaemonLogRotator 造出支撑 daemon.log 的按大小轮转 writer。轮转文件 gzip
// 压缩以压磁盘占用。daemon.log 的体积大头是 daemon 自己的 slog 输出加上 agent
// 子进程 stderr（都经 slog 转发），所以给它设上限基本就限住了全部增长。
func newDaemonLogRotator(logPath string) *lumberjack.Logger {
	return &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    envPositiveIntOrDefault("MULTICA_DAEMON_LOG_MAX_SIZE_MB", defaultDaemonLogMaxSizeMB),
		MaxBackups: envPositiveIntOrDefault("MULTICA_DAEMON_LOG_MAX_BACKUPS", defaultDaemonLogMaxBackups),
		MaxAge:     envPositiveIntOrDefault("MULTICA_DAEMON_LOG_MAX_AGE_DAYS", defaultDaemonLogMaxAgeDays),
		Compress:   true,
	}
}

// envPositiveIntOrDefault 读严格为正的整数环境变量；未设、空、损坏、为 0 或负数时
// 回退 def。把 0 当「用默认」是有意的：lumberjack 把 MaxSize=0 读作 100MB、
// MaxBackups=0 / MaxAge=0 读作「全保留」，照字面接受 0 会静默关掉保留、重新引入
// 无界增长。
func envPositiveIntOrDefault(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// openBoundedErrLog 在 daemon.err.log 长过 errLogMaxBytes 时先把它滚成一份 ".1"
// 备份，再以追加模式打开。这让原始崩溃落点在多次重启间有界（见 errLogMaxBytes），
// 又不必引入第二个轮转 writer——这个文件只会被继承的 fd 写入。
func openBoundedErrLog(path string) (*os.File, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() >= errLogMaxBytes {
		// 尽力滚动；忽略错误直接落到追加——改名失败绝不该挡 daemon 启动。
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// healthPortForProfile 返回给定 profile 的健康检查端口。默认 profile 用标准端口
// （19514）；命名 profile 由名字推导一个确定偏移。
func healthPortForProfile(profile string) int {
	if profile == "" {
		return daemon.DefaultHealthPort
	}
	// 简易散列：字节和 mod 1000，偏移从 base+1 起。
	var h int
	for _, b := range []byte(profile) {
		h += int(b)
	}
	return daemon.DefaultHealthPort + 1 + (h % 1000)
}

// daemonProfileMismatchError 表示：某 profile 健康端口上应答的 daemon 不是该
// profile 的 daemon。
type daemonProfileMismatchError struct {
	Want string // 调用方要操作的 profile
	Got  string // 该端口上 daemon 自报的 profile；Unreadable 时为空
	Port int
	// Unreadable 标记一个把 profile 字段答成了非字符串的 daemon。它声称了一个我们
	// 读不懂的身份，不能当作「我们的」来采信——见 daemonIdentityMismatch。
	Unreadable bool
}

// Error 把冲突渲染成人类可读的报错。
func (e *daemonProfileMismatchError) Error() string {
	if e.Unreadable {
		return fmt.Sprintf(
			"port %d is serving a daemon that reported an unreadable profile identity\n"+
				"Refusing to act on it as %s: an identity that cannot be read cannot be confirmed to be yours.",
			e.Port, describeProfile(e.Want))
	}
	return fmt.Sprintf(
		"port %d is serving profile %s, not %s\n"+
			"Both profile names hash to the same health port, so this command would have acted on the wrong daemon.\n"+
			"Run it against %s directly, or rename one of the profiles.",
		e.Port, describeProfile(e.Got), describeProfile(e.Want), describeProfile(e.Got))
}

// statusNote 是 `daemon status` 在结论下打印的一行说明——用于命令没失败、也没有
// 错误可渲染的场景。
func (e *daemonProfileMismatchError) statusNote() string {
	if e.Unreadable {
		return fmt.Sprintf("Note: port %d is serving a daemon whose profile identity could not be read.", e.Port)
	}
	return fmt.Sprintf("Note: port %d is serving %s, which hashes to the same port.",
		e.Port, describeProfile(e.Got))
}

// statusJSON 是 --output json 时附加的 `port_conflict` 对象。
func (e *daemonProfileMismatchError) statusJSON() map[string]any {
	if e.Unreadable {
		return map[string]any{"port": e.Port, "unreadable_identity": true}
	}
	return map[string]any{"port": e.Port, "profile": e.Got}
}

// describeProfile 把 profile 名渲染成给人看的文本：默认 profile 说全名而不是打空串。
func describeProfile(profile string) string {
	if profile == "" {
		return "the default profile"
	}
	return strconv.Quote(profile)
}

// daemonIdentityMismatch 判断 health 后面的 daemon 是否属于别的 profile（非当前
// 要操作的那个）。
//
// 健康端口是把 profile 名散列进 1000 端口范围得到的，所以不同名字也会撞车——
// 同一批字符换个顺序就落在同一端口。没有这个检查，`--profile a daemon stop` 会
// 读下「谁答了就把谁的 PID 杀掉」，撞车时杀的就是 profile b 的 daemon、以及它托管
// 的全部 runtime（#6694）。
//
// 早于 profile 字段出现的 daemon 会整个省略该字段。只有这种情形算「无法证明身份
// 也照跑」：在那拒绝会破坏对单纯老版本 daemon 的生命周期命令。一旦字段在场就严格
// 执行——包括空串（那是默认 profile 自报家门，不是缺失应答）。
func daemonIdentityMismatch(health map[string]any, profile string, port int) error {
	raw, ok := health["profile"]
	if !ok {
		return nil
	}
	got, isString := raw.(string)
	if !isString {
		// 在场但损坏（null、数字）与缺失不是一回事。去掉类型检查会把这种值坍缩成
		// ""——对操作默认 profile 的调用方来说就「匹配」了，等于把身份从未建立的
		// daemon 交到生命周期命令手里。缺失是 daemon 太老答不上来；这个是答错，
		// 按失败关闭处理。
		return &daemonProfileMismatchError{Want: profile, Port: port, Unreadable: true}
	}
	if got == profile {
		return nil
	}
	return &daemonProfileMismatchError{Want: profile, Got: got, Port: port}
}

// unknownProfileError 报告一个显式指名、但 ~/.multica/profiles 下没有对应目录的
// --profile。它带上已知 profile 名，让文本与 JSON 两种渲染都不用重新读盘就能列出。
type unknownProfileError struct {
	Profile string
	Known   []string
}

// Error 渲染成文本错误。
func (e *unknownProfileError) Error() string {
	if len(e.Known) == 0 {
		// 命令本身不加引号，让下方 shell 引号里的 profile 名成为整行唯一的
		// 引号处，提示粘贴后能原样跑通。
		return fmt.Sprintf("unknown profile %q: no named profiles exist yet.\nCreate one with: multica login --profile %s", e.Profile, shellQuoteArg(e.Profile))
	}
	return fmt.Sprintf("unknown profile %q\nKnown profiles: %s", e.Profile, strings.Join(e.Known, ", "))
}

// jsonPayload 渲染给 --output json 用。Known 保持非 nil 切片，让字段序列化成 []
// 而不是 null。
func (e *unknownProfileError) jsonPayload() map[string]any {
	known := e.Known
	if known == nil {
		known = []string{}
	}
	return map[string]any{
		"status":         "unknown_profile",
		"profile":        e.Profile,
		"known_profiles": known,
	}
}

// profileConfigFileName 是用于标识 profiles root 下某目录确实是个 profile（而非
// 只是某 profile 的父目录）的文件。与 cli.CLIConfigPathForProfile 保持一致。
const profileConfigFileName = "config.json"

// profileExists 判断显式指名的 profile 在磁盘上有没有状态。
//
// 名字走与配置层相同的路径逻辑解析，而不是拿扁平的目录列表去匹配——profile 名
// 可以含分隔符：`multica --profile team/dev config set ...` 会建
// ~/.multica/profiles/team/dev。按顶层条目匹配会拒掉这种 profile、却把它父目录
// "team" 当建议摆出来。
func profileExists(profile string) (bool, error) {
	dir, err := cli.ProfileDir(profile)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}

// knownProfiles 列出磁盘上存在的 profile，按名排序，供名字没命中时当建议。一个
// profile 是 profiles root 下持有 config.json 的目录，任意深度——强制要求该文件，
// 才能把 "team" 这类光秃秃的父目录挡在「每项都能直接当 --profile 用」的列表外。
//
// profiles root 缺失不是错误：只是还没有命名 profile——纯默认 profile 用户的常态。
func knownProfiles() ([]string, error) {
	root, err := profilesRootDir()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 读不了的子树只让我们少几条建议，不影响正确性：跳过它，剩下的列表
			// 照样到用户手里。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Name() != profileConfigFileName {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil || rel == "." {
			return nil
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			return nil, nil
		}
		return nil, walkErr
	}
	sort.Strings(names)
	return names, nil
}

// requireKnownProfile 拒绝显式指名但不存在于磁盘的 profile。
//
// healthPortForProfile 能把任意字符串散列成端口，所以没有这层检查，拼错的
// --profile 会去探测一个没绑任何东西的端口，命令报一句干巴巴的 "stopped"——与
// daemon 真没在跑无从区分，哪怕名字正确的 daemon 正在另一个端口好好服务（#6694）。
// 带着已知名字列表大声失败，把死胡同变成一眼就能修的问题。
//
// 默认 profile 永不校验：它直接拥有 ~/.multica、在 profiles/ 下没有条目，永远合法。
//
// 调用方必须在自己「daemon 任务守卫」（requireHumanLocalCommand，或 status 的
// daemonStatusHealthPort）之后调用。在任务里列出 profiles root 会泄露 Owner 的
// profile 名——正是那些守卫要防的。
func requireKnownProfile(profile string) error {
	if profile == "" {
		return nil
	}
	exists, err := profileExists(profile)
	if err != nil {
		return fmt.Errorf("resolve profile %q: %w", profile, err)
	}
	if exists {
		return nil
	}
	// 只有走到这里，盘上列举才值得做——也只有到这一步，它才可能以用户可见的
	// 方式出错。
	names, err := knownProfiles()
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	return &unknownProfileError{Profile: profile, Known: names}
}

// --- daemon start：启动 ---

// daemonExecutable 解析要 spawn 成后台 daemon 子进程的二进制。测试会覆盖它：
// 在测试里解析可执行文件会 fork 测试二进制本身，而它不认识 daemon 参数、只会把
// 测试套件再跑一遍。
var daemonExecutable = selfexec.Resolve

// requireDaemonAuth 在用户从未跑过 `multica login` 时快速失败。daemon 子进程本身
// 会做同样的检查（resolveAuth）并立即退出，但后台父进程看不见那个退出——它会对着
// 健康端口轮询满整个 45s 就绪窗口，然后打一句含糊的「查日志」。先检查就把这次
// 静默卡死变成一条立即可行动的报错。镜像 daemon.resolveAuth：daemon 只用配置里存
// 的 token 鉴权，绝不认 MULTICA_TOKEN。
func requireDaemonAuth(profile string) error {
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}
	if cfg.Token == "" {
		loginHint := "multica login"
		if profile != "" {
			loginHint = fmt.Sprintf("multica login --profile %s", profile)
		}
		return fmt.Errorf("you are not logged in. Run '%s' first, then start the daemon", loginHint)
	}
	return nil
}

// runDaemonStart 是 `daemon start` 的入口：按 --foreground 分流到前台或后台启动。
func runDaemonStart(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("daemon start"); err != nil {
		return err
	}
	foreground, _ := cmd.Flags().GetBool("foreground")
	if foreground {
		return runDaemonForeground(cmd)
	}
	return runDaemonBackground(cmd)
}

// runDaemonBackground 以后台子进程方式启动 daemon：解析健康端口、先探活并做身份
// 校验，登录态与可执行文件就绪后 spawn 子进程（原始输出进 err 日志），随后盯住
// 「早夭」信号与健康轮询直到 status=running，45 秒内起不来就给带日志摘录的报错。
func runDaemonBackground(cmd *cobra.Command) error {
	profile := resolveProfile(cmd)
	healthPort := healthPortForProfile(profile)

	// 检查 daemon 是否已在跑。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	health := checkDaemonHealthOnPort(ctx, healthPort)
	if daemonAlive(health) {
		// 端口上活着但属于别人的 daemon 是撞车，不是「已在运行」——再启会绑不上
		// 端口，而说「已在运行」会把用户引去一个根本不存在的自家 daemon。
		if err := daemonIdentityMismatch(health, profile, healthPort); err != nil {
			return err
		}
		label := "daemon"
		if profile != "" {
			label = fmt.Sprintf("daemon [%s]", profile)
		}
		pid, _ := health["pid"].(float64)
		return fmt.Errorf("%s is already running (pid %v). Use 'daemon restart' to restart it", label, int(pid))
	}

	if err := requireDaemonAuth(profile); err != nil {
		return err
	}

	// 解析当前可执行文件，让前台子进程复用本二进制。
	exePath, err := daemonExecutable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	// 组子进程参数：daemon start --foreground + 转发的 flag。
	args := buildDaemonStartArgs(cmd)

	// 确保 daemon 目录存在。
	dir := daemonDirForProfile(profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create daemon directory: %w", err)
	}

	// 子进程（前台 daemon）自己经轮转 writer 写 daemon.log；它继承的 stdout/stderr
	// 只需要接住原始崩溃输出，落到独立的非轮转 sink——这样绝不会把 daemon.log 的
	// 句柄长期占住（见 daemonStderrLogPathForProfile）。
	errLogPath := daemonStderrLogPathForProfile(profile)
	logFile, err := openBoundedErrLog(errLogPath)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", errLogPath, err)
	}
	// daemon 结构化日志真正落的地方（轮转），给下面的用户提示用。
	logPath := daemonLogPathForProfile(profile)

	// 记下两个日志现在的结尾偏移，早夭报告只展示「本次启动尝试」写的内容，而不是
	// 过期历史。err 日志的偏移在 openBoundedErrLog 之后取——它可能已把文件滚了一轮。
	var logOffset int64
	if info, statErr := os.Stat(logPath); statErr == nil {
		logOffset = info.Size()
	}
	var errLogOffset int64
	if info, statErr := os.Stat(errLogPath); statErr == nil {
		errLogOffset = info.Size()
	}

	child := exec.Command(exePath, args...)
	child.Stdout = logFile
	child.Stderr = logFile
	// Windows 上要把子进程从父 shell 的 Job Object 里拽出来，daemon 才能在父 shell
	// 退出后存活。父 Job 若没授予 BREAKAWAY_OK，CreateProcess 返回 ERROR_ACCESS_DENIED
	// ——退回不带 breakaway 启动，等同修复前行为。Unix 上这个 bool 是 no-op。
	child.SysProcAttr = daemonSysProcAttr(true)

	if err := child.Start(); err != nil {
		if isAccessDeniedSpawnErr(err) {
			// 不带 breakaway 重试。重置 cmd 状态——exec.Cmd 不能 Start() 两次，
			// 所以新建一个。
			child = exec.Command(exePath, args...)
			child.Stdout = logFile
			child.Stderr = logFile
			child.SysProcAttr = daemonSysProcAttr(false)
			if err := child.Start(); err != nil {
				logFile.Close()
				return fmt.Errorf("start daemon (no breakaway): %w", err)
			}
		} else {
			logFile.Close()
			return fmt.Errorf("start daemon: %w", err)
		}
	}
	logFile.Close()
	pid := child.Process.Pid

	// 盯住早夭而不是立刻 Release()：预检失败（server 不可达、token 401 被拒……）的
	// 子进程会在几秒内退出；没有这个信号，下面的就绪轮询会白白跑满整个窗口才打
	// 含糊的警告。goroutine 在 daemon 正常启动后会为进程剩余的不长寿命泄漏一次
	// Wait()——子进程不会被它杀掉、独立存活，与 Release() 效果相同。
	waitCh := make(chan error, 1)
	go func() { waitCh <- child.Wait() }()

	// 写 PID 文件。
	pidPath := daemonPIDPathForProfile(profile)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write PID file: %v\n", err)
	}

	// 轮询健康端点直到 daemon 报 ready（"running"）或超时。daemon 几乎立刻绑上
	// 健康端口，但 preflight 结束前一直报 status:"starting"（PAT 续期 + 初始工作区
	// 同步——会 exec 每个已配置 agent 做版本探测，冷缓存下可能要 ~20s）。窗口要留
	// 够，健康的冷启动才不会被误报成失败。
	const startupTimeout = 45 * time.Second
	deadline := time.Now().Add(startupTimeout)
	started := false
	lastStatus := ""
	for time.Now().Before(deadline) {
		select {
		case waitErr := <-waitCh:
			return daemonStartupFailureError(daemonStartupLogs{
				logPath:      logPath,
				logOffset:    logOffset,
				errLogPath:   errLogPath,
				errLogOffset: errLogOffset,
			}, waitErr, profile, resolveDaemonServerURL(cmd, profile))
		case <-time.After(500 * time.Millisecond):
		}
		hctx, hcancel := context.WithTimeout(context.Background(), 2*time.Second)
		health = checkDaemonHealthOnPort(hctx, healthPort)
		hcancel()
		lastStatus, _ = health["status"].(string)
		if lastStatus == "running" {
			started = true
			break
		}
	}
	if !started {
		if lastStatus == "starting" {
			fmt.Fprintf(os.Stderr, "Daemon is still starting after %s (agent detection / workspace sync is taking longer than expected). Check logs:\n  %s\n", startupTimeout, logPath)
		} else {
			fmt.Fprintf(os.Stderr, "Daemon may not have started successfully. Check logs:\n  %s\n  %s (crash output)\n", logPath, errLogPath)
		}
		return nil
	}

	if profile != "" {
		fmt.Fprintf(os.Stderr, "Daemon [%s] started (pid %d, version %s)\n", profile, pid, version)
	} else {
		fmt.Fprintf(os.Stderr, "Daemon started (pid %d, version %s)\n", pid, version)
	}
	fmt.Fprintf(os.Stderr, "Logs: %s\n", logPath)
	return nil
}

// resolveDaemonServerURL 解析 daemon 将连的 server URL，优先级与前台 daemon 一致：
// --server-url flag、MULTICA_SERVER_URL 环境变量、profile 的已存配置。
func resolveDaemonServerURL(cmd *cobra.Command, profile string) string {
	serverURL := cli.FlagOrEnv(cmd, "server-url", "MULTICA_SERVER_URL", "")
	if serverURL == "" {
		if c, err := cli.LoadCLIConfigForProfile(profile); err == nil && c.ServerURL != "" {
			serverURL = c.ServerURL
		}
	}
	return serverURL
}

// daemonStartupLogs 指名一次失败的启动尝试可能写过的两个 sink：子进程的结构化
// slog 输出落在轮转的 daemon.log，原始 stderr——Go panic 与最后一行的 cobra
// "Error: ..."——落在崩溃 sink（daemon.err.log）。两者都带 spawn 前记录的偏移，
// 让报告只覆盖本次尝试。
type daemonStartupLogs struct {
	logPath      string
	logOffset    int64
	errLogPath   string
	errLogOffset int64
}

// daemonStartupFailureError 把「还没报 ready 就退出的 daemon 子进程」变成友好、
// 可行动的报错。用户真实会撞的两种失败模式——配置的 token 被 server 拒、server
// 不可达——都从本次启动往结构化日志与崩溃 sink 追加的内容里认出来，渲染成一行
// 原因加一个下一步。其余情况回退到日志摘录（去掉 DBG/INF 噪音）。
func daemonStartupFailureError(logs daemonStartupLogs, waitErr error, profile, serverURL string) error {
	lines := readLogTailSince(logs.logPath, logs.logOffset, 40)
	crashLines := readLogTailSince(logs.errLogPath, logs.errLogOffset, 40)
	joined := strings.Join(append(append([]string{}, lines...), crashLines...), "\n")

	loginHint := "multica login"
	if profile != "" {
		loginHint += " --profile " + profile
	}

	switch {
	case strings.Contains(joined, "auth token rejected") ||
		strings.Contains(joined, "returned 401") ||
		strings.Contains(joined, "not authenticated"):
		return fmt.Errorf("daemon failed to start: the server rejected your login token (it may have expired or been revoked).\nRun '%s' to sign in again, then rerun 'multica daemon start'.\nFull log: %s", loginHint, logs.logPath)
	case strings.Contains(joined, "connection refused") ||
		strings.Contains(joined, "no such host") ||
		strings.Contains(joined, "i/o timeout") ||
		strings.Contains(joined, "network is unreachable"):
		target := "the Multica server"
		if serverURL != "" {
			target += " at " + serverURL
		}
		return fmt.Errorf("daemon failed to start: cannot reach %s.\nMake sure the server is running and reachable, then rerun 'multica daemon start'.\nFull log: %s", target, logs.logPath)
	}

	var b strings.Builder
	if exitErr := (&exec.ExitError{}); errors.As(waitErr, &exitErr) {
		fmt.Fprintf(&b, "daemon exited during startup (%s).", exitErr)
	} else {
		b.WriteString("daemon exited during startup.")
	}
	excerpt := filterDaemonLogNoise(lines, 5)
	// 崩溃输出本来就原始（panic、最后一行 cobra 错误），不过噪音过滤器，只像
	// 结构化摘录那样封顶行数。
	if len(crashLines) > 5 {
		crashLines = crashLines[len(crashLines)-5:]
	}
	excerpt = append(excerpt, crashLines...)
	if len(excerpt) > 0 {
		b.WriteString("\nLog output:")
		for _, line := range excerpt {
			fmt.Fprintf(&b, "\n  %s", line)
		}
	}
	fmt.Fprintf(&b, "\nFull log: %s", logs.logPath)
	if len(crashLines) > 0 {
		fmt.Fprintf(&b, "\nCrash output: %s", logs.errLogPath)
	}
	return errors.New(b.String())
}

// filterDaemonLogNoise 从摘录里丢掉 DBG/INF 日志行，保留 WRN/ERR 条目与非 slog
// 的普通行（比如子进程退出前打印的最后一条错误），并截到最近的 max 行。
func filterDaemonLogNoise(lines []string, max int) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, " DBG ") || strings.Contains(line, " INF ") {
			continue
		}
		out = append(out, line)
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// readLogTailSince 返回 sinceOffset 之后追加的日志内容（最多 maxLines 行）。读取
// 上限 64 KiB，防止突发的巨量输出把错误报告撑爆；任何读取问题都返回 nil，调用方
// 直接指到日志文件即可。
func readLogTailSince(logPath string, sinceOffset int64, maxLines int) []string {
	f, err := os.Open(logPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	const readCap = 64 * 1024
	if info, err := f.Stat(); err == nil && info.Size()-sinceOffset > readCap {
		sinceOffset = info.Size() - readCap
	}
	if _, err := f.Seek(sinceOffset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, readCap))
	if err != nil || len(data) == 0 {
		return nil
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]string, 0, maxLines)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) > maxLines {
		out = out[len(out)-maxLines:]
	}
	return out
}

// buildDaemonStartArgs 构造后台子进程的参数（daemon start --foreground + 转发各
// flag 的非默认值）。
func buildDaemonStartArgs(cmd *cobra.Command) []string {
	args := []string{"daemon", "start", "--foreground"}

	if v := flagString(cmd, "daemon-id"); v != "" {
		args = append(args, "--daemon-id", v)
	}
	if v := flagString(cmd, "device-name"); v != "" {
		args = append(args, "--device-name", v)
	}
	if v := flagString(cmd, "runtime-name"); v != "" {
		args = append(args, "--runtime-name", v)
	}
	if v := flagString(cmd, "workspaces-root"); v != "" {
		args = append(args, "--workspaces-root", v)
	}
	if d, _ := cmd.Flags().GetDuration("poll-interval"); d > 0 {
		args = append(args, "--poll-interval", d.String())
	}
	if d, _ := cmd.Flags().GetDuration("heartbeat-interval"); d > 0 {
		args = append(args, "--heartbeat-interval", d.String())
	}
	// agent-timeout 显式设置过才继续传，包括显式的 0（= 不设上限），这样它才能
	// 覆盖环境变量 MULTICA_AGENT_TIMEOUT。
	if cmd.Flags().Changed("agent-timeout") {
		d, _ := cmd.Flags().GetDuration("agent-timeout")
		args = append(args, "--agent-timeout", d.String())
	}
	if d, _ := cmd.Flags().GetDuration("codex-semantic-inactivity-timeout"); d > 0 {
		args = append(args, "--codex-semantic-inactivity-timeout", d.String())
	}
	if d, _ := cmd.Flags().GetDuration("codex-handshake-timeout"); d > 0 {
		args = append(args, "--codex-handshake-timeout", d.String())
	}
	if n, _ := cmd.Flags().GetInt("max-concurrent-tasks"); n > 0 {
		args = append(args, "--max-concurrent-tasks", strconv.Itoa(n))
	}
	if b, _ := cmd.Flags().GetBool("no-auto-update"); b {
		args = append(args, "--no-auto-update")
	}
	if d, _ := cmd.Flags().GetDuration("auto-update-interval"); d > 0 {
		args = append(args, "--auto-update-interval", d.String())
	}
	if b, _ := cmd.Flags().GetBool("no-auto-reload"); b {
		args = append(args, "--no-auto-reload")
	}

	// 转发全局持久 flag。
	if v, _ := cmd.Flags().GetString("server-url"); v != "" {
		args = append(args, "--server-url", v)
	}
	if v := resolveProfile(cmd); v != "" {
		args = append(args, "--profile", v)
	}

	return args
}

// runDaemonForeground 以前台方式运行 daemon 本体：解析 profile 配置与 flag/env/
// config 三级优先级的各旋钮，构造 daemon.Overrides 交给 daemon.LoadConfig 与
// daemon.New；写完 PID 文件后阻塞在 d.Run(ctx) 上。Run 正常返回且检测到磁盘上的
// 二进制版本变化（自更新）时，把后续移交给新二进制重启（见文件末尾的 handoff
// 逻辑）。
func runDaemonForeground(cmd *cobra.Command) error {
	util.EnsureHiddenConsole()

	profile := resolveProfile(cmd)

	// 读一次 profile 配置——CLI flag 与环境变量都没设时，不少 daemon 旋钮会回退到
	// 这里持久化的值。读配置出错对除 server URL 外的项都不致命：读不了/缺失只说明
	// 「没有持久化覆盖」，不说明「不能启动」。
	fileCfg, _ := cli.LoadCLIConfigForProfile(profile)
	// 选日志落点。用户在 shell 里跑 `daemon start --foreground` 时保留终端上实时、
	// 带色的日志（文档化的调试路径——见 docs troubleshooting）。被 launcher 把 stderr
	// 重定向到文件的脱离/后台子进程，则把结构化日志导进按大小有界、轮转的
	// daemon.log，让文件不会无限增长。靠 stderr 是否为终端来区分两者。
	var (
		logger     *slog.Logger
		logRotator *lumberjack.Logger
	)
	if logger_pkg.StderrIsTerminal() {
		logger = logger_pkg.NewLogger("daemon")
	} else {
		// 旧版 self-update launcher 可能把 daemon.log 本身当成 stdout/stderr 交给
		// 了本进程。Windows 上那个继承句柄缺 FILE_SHARE_DELETE，会挡轮转器改名，
		// 所以先把标准句柄重指向有界崩溃 sink，让 daemon.log 只归轮转器。Unix 上是
		// no-op——打开的 fd 从不挡改名。
		repointStdioToErrLog(daemonStderrLogPathForProfile(profile))
		logRotator = newDaemonLogRotator(daemonLogPathForProfile(profile))
		defer logRotator.Close()
		// 把本进程的结构化 logger 与包级 slog 默认都导进轮转器，让每一条日志
		// ——包括 LoadConfig 下面那句 slog.Warn——都被捕获并轮转。
		logger = logger_pkg.NewWriterLoggerDefault("daemon", logRotator)
	}

	serverURL := resolveDaemonServerURL(cmd, profile)
	// 每个可持久化的 daemon 旋钮都走同一个三级优先级：
	//
	//   --flag  >  MULTICA_… env  >  config.json  >  内置默认
	//
	// flag/env 这一对在 daemon.LoadConfig 内部解析（Overrides + envOrDefault）。
	// 这里只在 flag 与 env 都未设时才用配置文件的值回填——活跃的环境变量覆盖
	// （systemd unit 的常态）永远赢过文件。这跟上面 server_url 的优先级一致，
	// 解决了 #3824。
	deviceNameFlag := resolveDaemonStringOverride(
		flagString(cmd, "device-name"),
		"MULTICA_DAEMON_DEVICE_NAME",
		fileCfg.DeviceName,
	)
	runtimeNameFlag := resolveDaemonStringOverride(
		flagString(cmd, "runtime-name"),
		"MULTICA_AGENT_RUNTIME_NAME",
		fileCfg.RuntimeName,
	)
	workspacesRoot, err := resolveWorkspacesRootForProfile(profile, flagString(cmd, "workspaces-root"))
	if err != nil {
		return err
	}

	overrides := daemon.Overrides{
		ServerURL:      serverURL,
		DaemonID:       flagString(cmd, "daemon-id"),
		DeviceName:     deviceNameFlag,
		RuntimeName:    runtimeNameFlag,
		WorkspacesRoot: workspacesRoot,
		Profile:        profile,
		HealthPort:     healthPortForProfile(profile),
	}
	pollFlag, _ := cmd.Flags().GetDuration("poll-interval")
	pollOverride, err := resolveDaemonDurationOverride(pollFlag, "MULTICA_DAEMON_POLL_INTERVAL", fileCfg.PollInterval)
	if err != nil {
		return err
	}
	if pollOverride > 0 {
		overrides.PollInterval = pollOverride
	}
	heartbeatFlag, _ := cmd.Flags().GetDuration("heartbeat-interval")
	heartbeatOverride, err := resolveDaemonDurationOverride(heartbeatFlag, "MULTICA_DAEMON_HEARTBEAT_INTERVAL", fileCfg.HeartbeatInterval)
	if err != nil {
		return err
	}
	if heartbeatOverride > 0 {
		overrides.HeartbeatInterval = heartbeatOverride
	}
	// 区分「没传 flag」与显式 `--agent-timeout 0`，让用户能从 CLI 关掉 env 配的
	// 上限。persist 的 config.json 值也用同一个三态（见 cli.CLIConfig.AgentTimeout），
	// 于是 `config set agent_timeout 0s` 能扛过 daemon 重启。
	agentTimeoutOverride, err := resolveDaemonAgentTimeoutOverride(cmd, "MULTICA_AGENT_TIMEOUT", fileCfg.AgentTimeout)
	if err != nil {
		return err
	}
	if agentTimeoutOverride != nil {
		overrides.AgentTimeout = agentTimeoutOverride
	}
	semanticFlag, _ := cmd.Flags().GetDuration("codex-semantic-inactivity-timeout")
	semanticOverride, err := resolveDaemonDurationOverride(semanticFlag, "MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT", fileCfg.CodexSemanticInactivityTimeout)
	if err != nil {
		return err
	}
	if semanticOverride > 0 {
		overrides.CodexSemanticInactivityTimeout = semanticOverride
	}
	handshakeFlag, _ := cmd.Flags().GetDuration("codex-handshake-timeout")
	handshakeOverride, err := resolveDaemonDurationOverride(handshakeFlag, "MULTICA_CODEX_HANDSHAKE_TIMEOUT", fileCfg.CodexHandshakeTimeout)
	if err != nil {
		return err
	}
	if handshakeOverride > 0 {
		overrides.CodexHandshakeTimeout = handshakeOverride
	}
	maxFlag, _ := cmd.Flags().GetInt("max-concurrent-tasks")
	if n := resolveDaemonIntOverride(maxFlag, "MULTICA_DAEMON_MAX_CONCURRENT_TASKS", fileCfg.MaxConcurrentTasks); n > 0 {
		overrides.MaxConcurrentTasks = n
	}
	// --no-auto-update / MULTICA_DAEMON_AUTO_UPDATE=false / config.json 的
	// disable_auto_update 都是单向开关：任何一方都不能把自动更新强行打开，只能关。
	// env 为 "true" 在这里不当覆盖信号——肯定性场景由 LoadConfig 自己尊重原始 env。
	noAutoUpdateFlag, _ := cmd.Flags().GetBool("no-auto-update")
	if resolveDaemonDisableSignal(noAutoUpdateFlag, "MULTICA_DAEMON_AUTO_UPDATE", fileCfg.DisableAutoUpdate) {
		overrides.DisableAutoUpdate = true
	}
	// 磁盘版本监听器同是单向形态、走自己的 env：关掉 GitHub 轮询不能同时让 daemon
	// 不再跟随运维手动替换的二进制。
	noAutoReloadFlag, _ := cmd.Flags().GetBool("no-auto-reload")
	if resolveDaemonDisableSignal(noAutoReloadFlag, "MULTICA_DAEMON_AUTO_RELOAD", fileCfg.DisableAutoReload) {
		overrides.DisableAutoReload = true
	}
	autoUpdateFlag, _ := cmd.Flags().GetDuration("auto-update-interval")
	autoUpdateOverride, err := resolveDaemonDurationOverride(autoUpdateFlag, "MULTICA_DAEMON_AUTO_UPDATE_INTERVAL", fileCfg.AutoUpdateCheckInterval)
	if err != nil {
		return err
	}
	if autoUpdateOverride > 0 {
		overrides.AutoUpdateCheckInterval = autoUpdateOverride
	}

	cfg, err := daemon.LoadConfig(overrides)
	if err != nil {
		return err
	}
	cfg.CLIVersion = version
	cfg.CLICommit = buildCommit

	ctx, stop := notifyShutdownContext(context.Background())
	defer stop()

	d := daemon.New(cfg, logger)

	// 写 PID 文件，让 "daemon stop" 能找到我们。
	if dir := daemonDirForProfile(profile); dir != "" {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(daemonPIDPathForProfile(profile), []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	defer os.Remove(daemonPIDPathForProfile(profile))

	if err := d.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	// 检查 daemon 是否需要在 CLI 更新后重启。
	if restartBin := d.RestartBinary(); restartBin != "" {
		logger.Info("restarting daemon with updated binary", "path", restartBin)

		// 继任者会经自己的轮转 writer 打开 daemon.log，而 lumberjack 不支持两个
		// writer 共管一个文件。在继任者启动前停写我们的轮转器：关闭它，把本进程
		// 剩下的交接日志挪到崩溃 sink（stderr）——包括包级 slog 默认（否则它下次
		// 写入会重新打开 daemon.log）。
		if logRotator != nil {
			logger = logger_pkg.NewWriterLoggerDefault("daemon", os.Stderr)
			_ = logRotator.Close()
		}

		args := buildDaemonStartArgs(cmd)
		child := exec.Command(restartBin, args...)

		// 继任者是全新的前台 daemon，会经自己的轮转 writer 打开 daemon.log；把原始
		// stderr sink 交给它，继承的 fd 才不会占住 daemon.log（见 runDaemonBackground）。
		errLogPath := daemonStderrLogPathForProfile(profile)
		logFile, err := openBoundedErrLog(errLogPath)
		if err != nil {
			logger.Error("failed to open log file for restart", "error", err)
			// runtime 已在 handoff 前由 triggerRestart() 注销；supervisor 拉起的
			// 继任者会在启动时重新注册——这里不要再重复清理。
			return fmt.Errorf("failed to open daemon log file %s for restart: %w", errLogPath, err)
		}
		child.Stdout = logFile
		child.Stderr = logFile
		// Windows 上逃出父 Job Object；理由见 runDaemonBackground 调用处。
		child.SysProcAttr = daemonSysProcAttr(true)

		if err := child.Start(); err != nil {
			// triggerRestart() 在交接前已把 runtime 注销。supervisor 拉起的继任者
			// 会在启动时重新注册；别在这里重复清理。
			if isAccessDeniedSpawnErr(err) {
				child = exec.Command(restartBin, args...)
				child.Stdout = logFile
				child.Stderr = logFile
				child.SysProcAttr = daemonSysProcAttr(false)
				if err := child.Start(); err != nil {
					logFile.Close()
					logger.Error("failed to start new daemon (no breakaway)", "error", err)
					return fmt.Errorf("failed to start new daemon at %s without breakaway: %w", restartBin, err)
				}
			} else {
				logFile.Close()
				logger.Error("failed to start new daemon", "error", err)
				return fmt.Errorf("failed to start new daemon at %s: %w", restartBin, err)
			}
		}
		logFile.Close()
		child.Process.Release()

		// 写新 PID 文件。
		pidPath := daemonPIDPathForProfile(profile)
		os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o644)

		logger.Info("new daemon started", "pid", child.Process.Pid)
	}

	return nil
}

// --- daemon restart：重启 ---

// requireDaemonRestartPreflight 在停掉正在跑的 daemon 之前，先验证它的继任者真的
// 能起来。requireDaemonAuth 的空 token 检查不够：存着的 token 可能过期或已被吊销、
// server 可能不可达——两者都能过本地检查，却在 stop 阶段杀掉好端端的 daemon，然后
// 让继任子进程预检失败，最后一个 daemon 都不剩（#5165）。所以用存着的 token 探一下
// daemon 将连的 server（与 `multica auth status` 同一个 whoami 调用），失败就拒绝
// 碰正在运行的 daemon。
func requireDaemonRestartPreflight(cmd *cobra.Command, profile string) error {
	if err := requireDaemonAuth(profile); err != nil {
		return err
	}
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return fmt.Errorf("load CLI config: %w", err)
	}

	rawURL := resolveDaemonServerURL(cmd, profile)
	if rawURL == "" {
		rawURL = daemon.DefaultServerURL
	}
	baseURL, err := daemon.NormalizeServerBaseURL(rawURL)
	if err != nil {
		return fmt.Errorf("refusing to restart: invalid server URL %q: %w", rawURL, err)
	}

	loginHint := "multica login"
	if profile != "" {
		loginHint += " --profile " + profile
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if err := cli.NewAPIClient(baseURL, "", cfg.Token).GetJSON(ctx, "/api/me", nil); err != nil {
		var httpErr *cli.HTTPError
		if errors.As(err, &httpErr) {
			if httpErr.StatusCode == http.StatusUnauthorized {
				return fmt.Errorf("refusing to restart: the server rejected your login token (it may have expired or been revoked); the running daemon was left untouched.\nRun '%s' to sign in again, then rerun 'multica daemon restart'", loginHint)
			}
			return fmt.Errorf("refusing to restart: preflight check against %s failed (%w); the running daemon was left untouched", baseURL, err)
		}
		return fmt.Errorf("refusing to restart: cannot reach the Multica server at %s (%w); the running daemon was left untouched.\nMake sure the server is running and reachable, then rerun 'multica daemon restart'", baseURL, err)
	}
	return nil
}

// runDaemonRestart 执行 `daemon restart`：restart 会杀掉端口上应答的那个 daemon，
// 所以先做身份校验防撞车，再在 stop 阶段之前做一次 restart 预检（见
// requireDaemonRestartPreflight），随后优雅关停并等端口完全释放，最后正常 start。
func runDaemonRestart(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("daemon restart"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)
	if err := requireKnownProfile(profile); err != nil {
		return err
	}
	healthPort := healthPortForProfile(profile)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	health := checkDaemonHealthOnPort(ctx, healthPort)
	if daemonAlive(health) {
		// 身份校验先于预检、更先于 stop 阶段：restart 会杀掉端口上答应的东西，
		// 撞车会带走别人 profile 的 daemon、再在它的位置启动我们的。
		if err := daemonIdentityMismatch(health, profile, healthPort); err != nil {
			return err
		}
		// 在 stop 阶段之前（而不是只等 start 阶段）验证 restart 真能成功：
		// token 缺失/被吊销或 server 不可达，否则会先杀掉正在跑的 daemon、再启动
		// 继任者失败——最后什么都没有。什么都没在跑时没有可损失的东西，更便宜的
		// start 路径检查（空 token 守卫 + 子进程早夭探测）就够了，不必多这趟 server
		// 往返。
		if err := requireDaemonRestartPreflight(cmd, profile); err != nil {
			return err
		}
		pid, _ := health["pid"].(float64)
		if pid > 0 {
			fmt.Fprintf(os.Stderr, "Stopping daemon (pid %d)...\n", int(pid))
			if err := requestDaemonShutdown(healthPort); err != nil {
				if p, perr := os.FindProcess(int(pid)); perr == nil {
					_ = p.Kill()
				}
			}
			// 等到端口完全释放（不止「过了 running」）——否则下面新鲜的 start 会跟
			// 老 daemon 的监听器抢端口。
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				sctx, scancel := context.WithTimeout(context.Background(), 1*time.Second)
				h := checkDaemonHealthOnPort(sctx, healthPort)
				scancel()
				if !daemonAlive(h) {
					break
				}
			}
		}
	}

	// 从干净状态重新启动。
	return runDaemonStart(cmd, args)
}

// --- daemon stop：停止 ---

// runDaemonStop 执行 `daemon stop`：先身份校验（要杀的 PID 来自端口上应答者，信号
// 前先确认是我们的），再走 HTTP /shutdown 优雅关停（失败则强杀），随后轮询健康
// 端点直到进程消失。
func runDaemonStop(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("daemon stop"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)
	if err := requireKnownProfile(profile); err != nil {
		return err
	}
	healthPort := healthPortForProfile(profile)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	health := checkDaemonHealthOnPort(ctx, healthPort)
	if !daemonAlive(health) {
		label := "Daemon"
		if profile != "" {
			label = fmt.Sprintf("Daemon [%s]", profile)
		}
		fmt.Fprintf(os.Stderr, "%s is not running.\n", label)
		return nil
	}
	// 要杀的 PID 来自端口上答应的东西，发信号前先确认它是我们的。
	if err := daemonIdentityMismatch(health, profile, healthPort); err != nil {
		return err
	}

	pid, ok := health["pid"].(float64)
	if !ok || pid == 0 {
		return fmt.Errorf("could not determine daemon PID from health endpoint")
	}

	process, err := os.FindProcess(int(pid))
	if err != nil {
		return fmt.Errorf("find process %d: %w", int(pid), err)
	}

	// 走 daemon 的 HTTP /shutdown 端点优雅退出，而不是发 OS 信号。Windows 上
	// daemon 以 DETACHED_PROCESS 启动、与我们不共享控制台，GenerateConsoleCtrlEvent
	// 够不着它；HTTP 两个平台都通，且触发的是 daemon 自重启本就用的同一条
	// context-cancel 路径。
	if err := requestDaemonShutdown(healthPort); err != nil {
		fmt.Fprintf(os.Stderr, "Graceful shutdown request failed: %v — falling back to forced kill.\n", err)
		if kerr := process.Kill(); kerr != nil {
			return fmt.Errorf("kill daemon (pid %d): %w", int(pid), kerr)
		}
	}

	fmt.Fprintf(os.Stderr, "Stopping daemon (pid %d)...\n", int(pid))

	// 轮询健康端点直到 daemon 消失。
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 1*time.Second)
		h := checkDaemonHealthOnPort(ctx2, healthPort)
		cancel2()
		if !daemonAlive(h) {
			os.Remove(daemonPIDPathForProfile(profile))
			fmt.Fprintln(os.Stderr, "Daemon stopped.")
			return nil
		}
	}

	fmt.Fprintln(os.Stderr, "Daemon is still stopping. It may be finishing a running task.")
	return nil
}

// requestDaemonShutdown POST 到 daemon 的 /shutdown 端点请它优雅退出。请求送不到
// （网络错误、非 2xx、或端点早于本改动）时返回错误。
func requestDaemonShutdown(healthPort int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/shutdown", healthPort)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// --- daemon status：状态 ---

// runDaemonStatus 执行 `daemon status`：按 --output 输出 daemon 健康快照；健康端口
// 上若是别的 profile 的 daemon，则如实报告「本 profile 的 daemon 没在跑、端口被占」，
// 并把顶层结论保持 "stopped"，让读它的脚本不会错。
func runDaemonStatus(cmd *cobra.Command, _ []string) error {
	profile := resolveProfile(cmd)
	healthPort, err := daemonStatusHealthPort(cmd)
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")

	// 在这里列 profiles 是安全的：daemonStatusHealthPort 已拒绝了 daemon 任务里显式
	// 的 --profile。
	if err := requireKnownProfile(profile); err != nil {
		var unknown *unknownProfileError
		if output == "json" && errors.As(err, &unknown) {
			// 保持 stdout 是单个合法 JSON 文档。非零退出码承载失败，errSilent 压制
			// stderr 那份拷贝。
			if printErr := cli.PrintJSON(os.Stdout, unknown.jsonPayload()); printErr != nil {
				return printErr
			}
			return errSilent
		}
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	health := checkDaemonHealthOnPort(ctx, healthPort)

	// 别的 profile 的 daemon 在我们的端口上应答。把它报成「我们的」就是把同样的谎
	// 换个地方再说一遍：本 profile 的 daemon 并没在跑，只是端口被占。如实说，并保持
	// 顶层结论 "stopped"，让读它的脚本保持正确。
	//
	// 只在 daemon 管理任务之外做。任务里端口来自宿主 daemon 自己的注入而不是 profile
	// 散列，不可能撞车——而且任务的 profile 必然为空（--profile 被拒），宿主却可能跑
	// 着命名 profile，那样每个命名 profile 的宿主都会像「冲突」，破坏「任务里的
	// `daemon status` 报的是托管它的 daemon」这个长期约定。
	var conflict *daemonProfileMismatchError
	if daemonAlive(health) && !inDaemonTaskIdentityContext() {
		errors.As(daemonIdentityMismatch(health, profile, healthPort), &conflict)
	}

	if output == "json" {
		if conflict != nil {
			return cli.PrintJSON(os.Stdout, map[string]any{
				"status":        "stopped",
				"port_conflict": conflict.statusJSON(),
			})
		}
		return cli.PrintJSON(os.Stdout, health)
	}

	label := "Daemon"
	if profile != "" {
		label = fmt.Sprintf("Daemon [%s]", profile)
	}

	if conflict != nil {
		fmt.Fprintf(os.Stdout, "%s: stopped\n", label)
		fmt.Fprintln(os.Stdout, conflict.statusNote())
		return nil
	}

	switch health["status"] {
	case "running":
		printDaemonStatusReport(os.Stdout, label, health)
	case "starting":
		fmt.Fprintf(os.Stdout, "%s: starting (pid %v)\n", label, health["pid"])
	default:
		fmt.Fprintf(os.Stdout, "%s: stopped\n", label)
	}
	return nil
}

// daemonStatusHealthPort 决定 `status` 该探哪个 daemon。任务外就是 --profile 推导
// 的端口，即使宿主环境残留过期的 MULTICA_DAEMON_PORT。任务内则只认 daemon 注入的
// 端口：healthPortForProfile 会给任何传入的 profile 名散列端口，在那推端口会让任务
// 报到并非托管它的 daemon 头上——而且被命名 profile 的 daemon 托管的任务会悄悄去
// 探默认 daemon。端口缺失按失败关闭处理，不猜。
func daemonStatusHealthPort(cmd *cobra.Command) (int, error) {
	profile := resolveProfile(cmd)
	if !inDaemonTaskIdentityContext() {
		return healthPortForProfile(profile), nil
	}
	if profile != "" {
		return 0, fmt.Errorf("daemon status --profile is not available inside a daemon-managed task")
	}
	raw := strings.TrimSpace(os.Getenv("MULTICA_DAEMON_PORT"))
	if raw == "" {
		return 0, fmt.Errorf("daemon status inside a daemon-managed task requires the daemon-injected MULTICA_DAEMON_PORT")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("invalid MULTICA_DAEMON_PORT %q inside a daemon-managed task", raw)
	}
	return port, nil
}

// printDaemonStatusReport 渲染 daemon 健康响应的键值摘要。值列对齐到最宽标签，
// 动态的 "Daemon [profile]" 行才跟下面静态行对齐。
func printDaemonStatusReport(w io.Writer, label string, health map[string]any) {
	type row struct{ key, value string }
	rows := []row{
		{label, fmt.Sprintf("running (pid %v, uptime %v)", health["pid"], health["uptime"])},
	}
	if version, ok := health["cli_version"].(string); ok && version != "" {
		rows = append(rows, row{"Version", version})
	}
	// 只在确认的磁盘版本变更等 daemon 空闲时出现，读起来是解释而不是状态行。
	if reason, ok := health["reload_pending_reason"].(string); ok && reason != "" {
		rows = append(rows, row{"Restart pending", reason})
	}
	if agents, ok := health["agents"].([]any); ok && len(agents) > 0 {
		parts := make([]string, len(agents))
		for i, a := range agents {
			parts[i] = fmt.Sprint(a)
		}
		rows = append(rows, row{"Agents", strings.Join(parts, ", ")})
	}
	if ws, ok := health["workspaces"].([]any); ok {
		rows = append(rows, row{"Workspaces", strconv.Itoa(len(ws))})
	}

	keyWidth := 0
	for _, r := range rows {
		if n := len(r.key); n > keyWidth {
			keyWidth = n
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%-*s  %s\n", keyWidth+1, r.key+":", r.value)
	}
}

// --- daemon logs：日志 ---

// tailLog 是测试替换的接缝——不 spawn 平台 tail 实现也能观察 `daemon logs`。
var tailLog = tailLogFile

// profileLabel 给人读地给 profile 起名字；默认 profile 没有名字。
func profileLabel(profile string) string {
	if profile == "" {
		return "default"
	}
	return profile
}

// daemonLogSourcePath 解析某个 profile 的 daemon.log 路径——默认 profile 是
// ~/.multica/daemon.log，命名 profile 是 ~/.multica/profiles/<name>/daemon.log——
// 并保证返回的是绝对路径，因为该路径要展示给用户、供其粘进编辑器或另一个 shell。
//
// 如果结果成了相对路径，说明 daemonDirForProfile 没能解析出 HOME 目录并把错误
// 吞了，只留下一个光秃秃的 "daemon.log"。此时要报出来而不是返回它：返回的话它
// 会相对调用方的 cwd 解析，指向一个与 daemon 毫不相干的文件。
func daemonLogSourcePath(profile string) (string, error) {
	logPath := daemonLogPathForProfile(profile)
	if !filepath.IsAbs(logPath) {
		return "", fmt.Errorf("cannot resolve the state directory for profile %q", profileLabel(profile))
	}
	return logPath, nil
}

func runDaemonLogs(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("daemon logs"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)
	if err := requireKnownProfile(profile); err != nil {
		return err
	}
	logPath, err := daemonLogSourcePath(profile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return fmt.Errorf("no log file found at %s\nThe daemon may not have been started in background mode", logPath)
	}

	follow, _ := cmd.Flags().GetBool("follow")
	lines, _ := cmd.Flags().GetInt("lines")

	// 流式输出前先把文件名说出来。哪个 daemon.log 是活的本来无从得知——一份陈旧的
	// 默认 profile 日志读起来很正常、显得很新——而这条命令是唯一知道答案的地方。它
	// 走 stderr、在 tail 开始前写，让 `-f` 输出与下游管道保持干净。
	fmt.Fprintf(cmd.ErrOrStderr(), "Reading %s (profile: %s)\n", logPath, profileLabel(profile))

	return tailLog(logPath, lines, follow)
}

// daemonAlive 判断一份健康响应是否表明端口上有活的 daemon 进程——要么完全
// "running"（就绪），要么还在 "starting"（端口已绑、preflight 进行中）。只想知道
// 「有没有 daemon 在」（已运行守卫、restart、stop）的生命周期命令用它；而
// `daemon start` 的就绪等待以更严格的 "running" 为准。
func daemonAlive(health map[string]any) bool {
	switch health["status"] {
	case "running", "starting":
		return true
	default:
		return false
	}
}

// checkDaemonHealthOnPort 调给定端口上的 daemon 本地健康端点；任何失败都归一成
// {"status":"stopped"} 而不是让调用方处理网络错误。
func checkDaemonHealthOnPort(ctx context.Context, port int) map[string]any {
	addr := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return map[string]any{"status": "stopped"}
	}

	httpClient := &http.Client{Timeout: 2 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return map[string]any{"status": "stopped"}
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return map[string]any{"status": "stopped"}
	}
	return result
}

// flagString 取字符串 flag 值，空则返回空串。
func flagString(cmd *cobra.Command, name string) string {
	val, _ := cmd.Flags().GetString(name)
	return val
}

// envUnset 判断给定环境变量是否为空或整个缺失。daemon 旋钮的三级优先级
// （flag > env > config.json）就靠这个判断：只有当 flag 与 env 都没设时配置文件
// 回填才生效。trim 空白与 daemon.envOrDefault 行为一致——被导出成 " " 的 env 在
// 回退层面读作「未设置」，与运行时相同。
func envUnset(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) == ""
}

// resolveDaemonStringOverride 选出写进 daemon.Overrides 的字符串旋钮值
// （device_name、runtime_name、workspaces_root）。优先级从高到低：
//
//  1. flag（已是字符串；空 = 未传）
//  2. env（已设则 overrides 留空——daemon.LoadConfig 自己读 env）
//  3. cfg（persist 的 config.json 值）
//  4. ""（overrides 留空——daemon.LoadConfig 套默认）
//
// 拆出来是为了不拉起 cobra + 整套 daemon.LoadConfig 管线也能测优先级。返回值是
// 调用方应赋的覆盖值；空串意味着「让 daemon.LoadConfig 定」。
func resolveDaemonStringOverride(flagValue, envName, cfgValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if !envUnset(envName) {
		// Env 已设——返回空串，让 daemon.LoadConfig 的 envOrDefault 来解析。这里若
		// 返回 env 值等于读了两次，一旦 daemon 自己的 trim 逻辑变了两边会悄悄分叉。
		return ""
	}
	return cfgValue
}

// resolveWorkspacesRootForProfile 是 daemon 任务工作区根目录唯一的人类 CLI 解析器。
// 让 daemon start、disk-usage、聚合扫描与跨 profile 提示都走这条路，诊断才不会偏离
// daemon 实际用的目录。
func resolveWorkspacesRootForProfile(profile, flagValue string) (string, error) {
	fileCfg, _ := cli.LoadCLIConfigForProfile(profile)
	override := resolveDaemonStringOverride(
		flagValue,
		"MULTICA_WORKSPACES_ROOT",
		fileCfg.WorkspacesRoot,
	)
	return daemon.ResolveWorkspacesRoot(profile, override)
}

// resolveDaemonDurationOverride 是 poll_interval 等数值旋钮的解析。flagValue > 0
// 优先；否则在 env 未设时解析 cfgValue。解析错误与非正值都如实抛出，坏的
// config.json 值才不会悄悄落回默认——`config set` 写入时本就拒掉同样形态，这里补
// 一刀接住早于该检查的历史配置。
func resolveDaemonDurationOverride(flagValue time.Duration, envName, cfgValue string) (time.Duration, error) {
	if flagValue > 0 {
		return flagValue, nil
	}
	if !envUnset(envName) || cfgValue == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(cfgValue)
	if err != nil {
		return 0, fmt.Errorf("config value %q for %s is not a valid duration: %w", cfgValue, envName, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("config value %q for %s must be positive", cfgValue, envName)
	}
	return parsed, nil
}

// resolveDaemonIntOverride 是 max_concurrent_tasks 的解析：flagValue > 0 优先；
// 否则只有 env 未设时配置值才生效。
func resolveDaemonIntOverride(flagValue int, envName string, cfgValue int) int {
	if flagValue > 0 {
		return flagValue
	}
	if !envUnset(envName) {
		return 0
	}
	if cfgValue > 0 {
		return cfgValue
	}
	return 0
}

// resolveDaemonAgentTimeoutOverride 解析 --agent-timeout：走与其他旋钮相同的三级
// 优先级，但保留 agent_timeout 的三态语义——nil 结果 = 「未覆盖」，非 nil = 「用
// 这个确切值」（合法地可以是 0，用于关掉墙钟上限）。
//
//  1. --agent-timeout 显式传过（哪怕 `--agent-timeout 0`）→ 用该值；LoadConfig
//     不再看 env/默认。
//  2. MULTICA_AGENT_TIMEOUT 已设 → 返回 nil 让 LoadConfig 自己读 env（与其他
//     duration 辅助一致）。
//  3. cfgValue 非 nil → 解析持久化串并采用。"0s" 在这里合法（"禁用"哨兵）。
//  4. 否则 → nil，LoadConfig 用 DefaultAgentTimeout。
func resolveDaemonAgentTimeoutOverride(cmd *cobra.Command, envName string, cfgValue *string) (*time.Duration, error) {
	if cmd.Flags().Changed("agent-timeout") {
		d, _ := cmd.Flags().GetDuration("agent-timeout")
		return &d, nil
	}
	if !envUnset(envName) {
		return nil, nil
	}
	if cfgValue == nil || *cfgValue == "" {
		return nil, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(*cfgValue))
	if err != nil {
		return nil, fmt.Errorf("config value %q for %s is not a valid duration: %w", *cfgValue, envName, err)
	}
	if parsed < 0 {
		return nil, fmt.Errorf("config value %q for %s must be >= 0", *cfgValue, envName)
	}
	return &parsed, nil
}

// resolveDaemonDisableSignal 解析单向「禁用」信号——自动更新（--no-auto-update）
// 与自动重载（--no-auto-reload）同形。优先级（以自动更新为例）：
//
//  1. 传了 --no-auto-update flag → 禁用。
//  2. MULTICA_DAEMON_AUTO_UPDATE 显式设成假值 → 禁用。false/0/no/off 以外的 env
//     值在 daemon.LoadConfig 内部处理（可能在 self-host 上启用自动更新，那是今天
//     的 CLI overrides 表达不了的东西）。
//  3. config.json 的 disable_auto_update=true（仅当 flag 与 env 都沉默）→ 禁用。
//  4. 否则 → 覆盖保持关，LoadConfig 挑默认。
func resolveDaemonDisableSignal(flagValue bool, envName string, cfgValue bool) bool {
	if flagValue {
		return true
	}
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		switch strings.ToLower(v) {
		case "false", "0", "no", "off":
			return true
		}
		// Env 设成了假值以外的内容——覆盖保持关，让 LoadConfig 自己读原始 env。
		return false
	}
	return cfgValue
}

// --- daemon disk-usage：磁盘占用 ---

// runDaemonDiskUsage 执行 `daemon disk-usage`：扫描工作区根目录并按任务/工作区
// 聚合输出；任务上下文只允许扫宿主 daemon 的根（checkTaskDiskUsageScope），支持
// --all-profiles 聚合与 --top 截断。STATUS 列尽力而为，网络不可用时留空。
func runDaemonDiskUsage(cmd *cobra.Command, _ []string) error {
	taskContext := inDaemonManagedExecutionContext()
	profile := resolveProfile(cmd)
	rootOverride, _ := cmd.Flags().GetString("workspaces-root")
	byWorkspace, _ := cmd.Flags().GetBool("by-workspace")
	byTask, _ := cmd.Flags().GetBool("by-task")
	top, _ := cmd.Flags().GetInt("top")
	output, _ := cmd.Flags().GetString("output")
	allProfiles, _ := cmd.Flags().GetBool("all-profiles")

	if taskContext {
		if err := checkTaskDiskUsageScope(profile, rootOverride, allProfiles); err != nil {
			return err
		}
	}
	if byWorkspace && byTask {
		return fmt.Errorf("--by-workspace and --by-task are mutually exclusive")
	}
	if top < 0 {
		return fmt.Errorf("--top must be a non-negative integer")
	}
	if allProfiles && rootOverride != "" {
		return fmt.Errorf("--all-profiles and --workspaces-root are mutually exclusive")
	}

	if allProfiles {
		return runDaemonDiskUsageAggregate(cmd, byWorkspace, top, output)
	}

	workspacesRoot, err := resolveDiskUsageRoot(taskContext, profile, rootOverride)
	if err != nil {
		return fmt.Errorf("resolve workspaces root: %w", err)
	}

	report, err := daemon.ScanDiskUsage(workspacesRoot, daemon.ArtifactPatternsFromEnv())
	if err != nil {
		return err
	}
	// 在 --top 截断前解析状态：批量端点两头成本一样，JSON 消费方则想为拿到的
	// 每项都看到状态。任务内跳过：fetcher 用 Owner profile 的已存 token 鉴权，
	// 任务不能借用，所以那列留空。
	if !taskContext && diskUsageNeedsParentStatus(byWorkspace, output) {
		fillDiskUsageParentStatuses(cmd, profile, &report)
	}

	if top > 0 {
		if byWorkspace {
			if top < len(report.Workspaces) {
				report.Workspaces = report.Workspaces[:top]
			}
		} else if top < len(report.Tasks) {
			report.Tasks = report.Tasks[:top]
		}
	}

	if output == "json" {
		return cli.PrintJSON(os.Stdout, report)
	}

	if byWorkspace {
		printDiskUsageWorkspaceTable(os.Stdout, report)
		printDiskUsageOtherRootsHint(os.Stdout, report, profile, rootOverride, taskContext)
		return nil
	}
	printDiskUsageTaskTable(os.Stdout, report)
	printDiskUsageOtherRootsHint(os.Stdout, report, profile, rootOverride, taskContext)
	return nil
}

// checkTaskDiskUsageScope 把托管任务里 disk-usage 的视野限制在托管它的 daemon 根
// 目录内。下列输入都会把报告撑过那道边界：--all-profiles 枚举 ~/.multica/profiles
// 会泄露 Owner 的 profile 名；--workspaces-root 与 --profile 则把扫描指向本 daemon
// 不管的目录。
func checkTaskDiskUsageScope(profile, rootOverride string, allProfiles bool) error {
	switch {
	case allProfiles:
		return fmt.Errorf("daemon disk-usage --all-profiles is not available inside a daemon-managed task")
	case rootOverride != "":
		return fmt.Errorf("daemon disk-usage --workspaces-root is not available inside a daemon-managed task")
	case profile != "":
		return fmt.Errorf("daemon disk-usage --profile is not available inside a daemon-managed task")
	}
	return nil
}

// resolveDiskUsageRoot 选要扫描的目录。托管任务只读 daemon 注入的根：
// ResolveWorkspacesRoot 会回退到 $HOME 与 profile 推导的默认——对命名 profile
// daemon 托管的任务，那会落到默认根、报一棵任务毫无关系的树。值缺失时失败关闭，
// 杜绝那种瞎猜进输出。
func resolveDiskUsageRoot(taskContext bool, profile, rootOverride string) (string, error) {
	if !taskContext {
		return resolveWorkspacesRootForProfile(profile, rootOverride)
	}
	root := strings.TrimSpace(os.Getenv(daemon.TaskWorkspacesRootEnv))
	if root == "" {
		return "", fmt.Errorf("daemon-managed task requires the daemon-injected workspaces root in %s", daemon.TaskWorkspacesRootEnv)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%s must be an absolute path", daemon.TaskWorkspacesRootEnv)
	}
	return filepath.Clean(root), nil
}

// diskUsageNeedsParentStatus 判断本次调用是否要渲染任务行。--by-workspace 表格
// 没有 STATUS 列，为它解析状态会白花一次网络往返——server 不可达时还会等满超时——
// 把本地诊断变成离线就挂的命令。JSON 输出总带任务数组，所以即便 --by-workspace
// 也需要状态。
func diskUsageNeedsParentStatus(byWorkspace bool, output string) bool {
	return !byWorkspace || output == "json"
}

// fillDiskUsageParentStatuses 就地填报告里的 STATUS 列。
//
// 尽力而为、绝不因此让命令失败：`disk-usage` 是本地诊断，离线或登出时也得能跑。
// 状态解析不了就让该列保持空白，原因打 stderr——`--output json` 的 stdout 保持
// 机器可读。
func fillDiskUsageParentStatuses(cmd *cobra.Command, profile string, report *daemon.DiskUsageReport) {
	fetch := newParentStatusFetcher(cmd, profile)
	if fetch == nil {
		return
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	if err := daemon.ResolveParentStatuses(ctx, report, fetch); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not resolve issue statuses, STATUS column may be blank: %v\n", err)
	}
}

// newParentStatusFetcher 造一个由 daemon GC 循环同款批量 gc-check 端点支撑的
// fetcher，让 STATUS 列精确报告「GC 真会按它行动」的状态。daemon 用 profile 的
// CLI token 鉴权（见 Daemon.resolveAuth），这里复用它无需新开 API 面。该 profile
// 没有可用凭据时返回 nil——调用方于是留空列而不是报错。
func newParentStatusFetcher(cmd *cobra.Command, profile string) daemon.ParentStatusFetcher {
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil || strings.TrimSpace(cfg.Token) == "" {
		return nil
	}
	rawURL := resolveDaemonServerURL(cmd, profile)
	if rawURL == "" {
		rawURL = daemon.DefaultServerURL
	}
	baseURL, err := daemon.NormalizeServerBaseURL(rawURL)
	if err != nil {
		return nil
	}

	client := daemon.NewClient(baseURL)
	client.SetToken(cfg.Token)
	return func(ctx context.Context, workspaceID string, issueIDs []string) (map[string]string, error) {
		results, err := client.GetIssueGCChecks(ctx, workspaceID, issueIDs)
		if err != nil {
			return nil, err
		}
		statuses := make(map[string]string, len(results))
		for id, result := range results {
			// 跳过逐 issue 失败与 404：解析不了的 id 必须保持空白，读作「未知」
			// 而不是某个状态。
			if result.Err != nil || !result.Found {
				continue
			}
			statuses[id] = result.Status
		}
		return statuses, nil
	}
}

// runDaemonDiskUsageAggregate 扫描每个工作区根（默认根 + 每个 ~/.multica/profiles/*
// 根）并渲染带合计总和的 per-root 分解。
func runDaemonDiskUsageAggregate(cmd *cobra.Command, byWorkspace bool, top int, output string) error {
	roots, err := enumerateDiskUsageRoots()
	if err != nil {
		return err
	}
	agg, err := daemon.ScanDiskUsageRoots(roots, daemon.ArtifactPatternsFromEnv())
	if err != nil {
		return err
	}
	// 每个根属于它自己的 profile，需要各自的 token。没有行要渲染状态时整圈跳过
	// 最重要：server 不可达会按根串行烧掉一次完整超时。
	if diskUsageNeedsParentStatus(byWorkspace, output) {
		for i := range agg.Roots {
			fillDiskUsageParentStatuses(cmd, agg.Roots[i].Profile, &agg.Roots[i].Report)
		}
	}

	// --top 独立截每个根的表格——报告的总和始终锚定完整扫描，与单根 --top 一致。
	if top > 0 {
		for i := range agg.Roots {
			r := &agg.Roots[i].Report
			if byWorkspace {
				if top < len(r.Workspaces) {
					r.Workspaces = r.Workspaces[:top]
				}
			} else if top < len(r.Tasks) {
				r.Tasks = r.Tasks[:top]
			}
		}
	}

	if output == "json" {
		return cli.PrintJSON(os.Stdout, agg)
	}
	printAggregateDiskUsage(os.Stdout, agg, byWorkspace)
	return nil
}

// enumerateDiskUsageRoots 返回 --all-profiles 模式要扫的工作区根集合：默认根排
// 最前（无论是否为空都列，作方位参考），再按 profile 名排序，逐个接上磁盘上
// 存在的 ~/.multica/profiles/* 根。解析到同一路径的根（如 MULTICA_WORKSPACES_ROOT
// 把每个 profile 都钉到一个目录）合并成单条。
func enumerateDiskUsageRoots() ([]daemon.DiskUsageRoot, error) {
	out := make([]daemon.DiskUsageRoot, 0)

	if root, err := resolveWorkspacesRootForProfile("", ""); err == nil {
		out = append(out, daemon.DiskUsageRoot{Profile: "", Root: root})
	}

	profilesRoot, err := profilesRootDir()
	if err != nil {
		return out, nil
	}
	entries, err := os.ReadDir(profilesRoot)
	if err != nil {
		return out, nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		root, err := resolveWorkspacesRootForProfile(name, "")
		if err != nil || containsDiskUsageRoot(out, root) {
			continue
		}
		// 跳过磁盘上从没建过的 profile 根——配了却没跑过 daemon 的 profile 没东西
		// 可报。
		if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
			continue
		}
		out = append(out, daemon.DiskUsageRoot{Profile: name, Root: root})
	}
	return out, nil
}

// containsDiskUsageRoot 判断 candidate 是否已在 roots 里（按路径相等比较）。
func containsDiskUsageRoot(roots []daemon.DiskUsageRoot, candidate string) bool {
	for _, root := range roots {
		if samePath(root.Root, candidate) {
			return true
		}
	}
	return false
}

// printAggregateDiskUsage 渲染聚合扫描结果：每根一段、末尾合计（含各根 .repos
// 仓库缓存计数）。
func printAggregateDiskUsage(w io.Writer, agg daemon.AggregateDiskUsageReport, byWorkspace bool) {
	fmt.Fprintf(w, "Scanned %d workspace root(s).\n", len(agg.Roots))
	for _, root := range agg.Roots {
		fmt.Fprintln(w)
		label := "default"
		if root.Profile != "" {
			label = root.Profile
		}
		fmt.Fprintf(w, "[%s]\n", label)
		if byWorkspace {
			printDiskUsageWorkspaceTable(w, root.Report)
		} else {
			printDiskUsageTaskTable(w, root.Report)
		}
	}
	fmt.Fprintf(w, "\nGrand total: %s across %d task(s) in %d root(s); %s reclaimable as artifacts (%.1f%%).\n",
		formatBytes(agg.TotalSizeBytes), agg.TotalTaskCount, len(agg.Roots),
		formatBytes(agg.TotalArtifactSizeBytes), agg.TotalArtifactRatio*100)
	if agg.TotalRepoCacheCount > 0 || agg.TotalRepoCacheSizeBytes > 0 {
		fmt.Fprintf(w, "Repo cache (.repos): %s across %d repo(s) in all roots, not included above.\n",
			formatBytes(agg.TotalRepoCacheSizeBytes), agg.TotalRepoCacheCount)
	}
}

// printDiskUsageTaskTable 渲染按任务聚合的表格；--top 截断时把「显示的行」与
// 「全量扫描的总和」分开标注。
func printDiskUsageTaskTable(w io.Writer, report daemon.DiskUsageReport) {
	fmt.Fprintf(w, "Workspaces root: %s\n", report.WorkspacesRoot)
	if report.TotalTaskCount == 0 {
		fmt.Fprintln(w, "(no task directories)")
		printRepoCacheLine(w, report)
		return
	}
	rows := make([][]string, 0, len(report.Tasks))
	var displayedSize, displayedArtifact int64
	for _, task := range report.Tasks {
		displayedSize += task.SizeBytes
		displayedArtifact += task.ArtifactSizeBytes
		rows = append(rows, []string{
			task.WorkspaceShort + "/" + task.TaskShort,
			task.Kind,
			emptyDash(task.ParentStatus),
			formatAge(task.AgeSeconds),
			formatBytes(task.SizeBytes),
			formatBytes(task.ArtifactSizeBytes),
		})
	}
	cli.PrintTable(w, []string{"PATH", "KIND", "STATUS", "AGE", "SIZE", "ARTIFACTS"}, rows)

	if len(report.Tasks) < report.TotalTaskCount {
		// 报告级总和锚定完整扫描；显示的行是用户正看的。分开标注，`--top N` 才不会
		// 在一眼扫过的分类里误导人。
		fmt.Fprintf(w, "\nShowing top %d of %d task(s). Displayed: %s (%s artifacts). Scan total: %s (%s artifacts, %.1f%% reclaimable).\n",
			len(report.Tasks), report.TotalTaskCount,
			formatBytes(displayedSize), formatBytes(displayedArtifact),
			formatBytes(report.TotalSizeBytes), formatBytes(report.TotalArtifactSizeBytes),
			report.TotalArtifactRatio*100)
	} else {
		fmt.Fprintf(w, "\nTotal: %s across %d task(s); %s reclaimable as artifacts (%.1f%%).\n",
			formatBytes(report.TotalSizeBytes), report.TotalTaskCount,
			formatBytes(report.TotalArtifactSizeBytes), report.TotalArtifactRatio*100)
	}
	printRepoCacheLine(w, report)
}

// printRepoCacheLine 单独一行报裸仓库缓存。工作区里每个任务目录都从这份共享缓存
// 检出，把它并进任务总和会把占用算进不含它的目录；而完全省略——旧行为——正是
// 让报告总和与用户文件管理器静默不一致的原因。
func printRepoCacheLine(w io.Writer, report daemon.DiskUsageReport) {
	if report.RepoCacheCount == 0 && report.RepoCacheSizeBytes == 0 {
		return
	}
	fmt.Fprintf(w, "Repo cache (.repos): %s across %d repo(s), not included above. Evicted once a repo is unused for MULTICA_GC_REPO_TTL and no longer attached to a workspace.\n",
		formatBytes(report.RepoCacheSizeBytes), report.RepoCacheCount)
}

// printDiskUsageWorkspaceTable 渲染按工作区聚合的表格（无 STATUS 列）。
func printDiskUsageWorkspaceTable(w io.Writer, report daemon.DiskUsageReport) {
	fmt.Fprintf(w, "Workspaces root: %s\n", report.WorkspacesRoot)
	if report.TotalWorkspaceCount == 0 {
		fmt.Fprintln(w, "(no workspaces)")
		printRepoCacheLine(w, report)
		return
	}
	rows := make([][]string, 0, len(report.Workspaces))
	var displayedSize, displayedArtifact int64
	for _, ws := range report.Workspaces {
		displayedSize += ws.SizeBytes
		displayedArtifact += ws.ArtifactSizeBytes
		rows = append(rows, []string{
			ws.WorkspaceShort,
			strconv.Itoa(ws.TaskCount),
			formatBytes(ws.SizeBytes),
			formatBytes(ws.ArtifactSizeBytes),
			formatRatio(ws.ArtifactRatio),
			formatAge(ws.OldestAgeSeconds),
		})
	}
	cli.PrintTable(w, []string{"WORKSPACE", "TASKS", "SIZE", "ARTIFACTS", "ARTIFACT %", "OLDEST"}, rows)

	if len(report.Workspaces) < report.TotalWorkspaceCount {
		fmt.Fprintf(w, "\nShowing top %d of %d workspace(s). Displayed: %s (%s artifacts). Scan total: %s (%s artifacts, %.1f%% reclaimable).\n",
			len(report.Workspaces), report.TotalWorkspaceCount,
			formatBytes(displayedSize), formatBytes(displayedArtifact),
			formatBytes(report.TotalSizeBytes), formatBytes(report.TotalArtifactSizeBytes),
			report.TotalArtifactRatio*100)
	} else {
		fmt.Fprintf(w, "\nTotal: %s across %d workspace(s); %s reclaimable as artifacts (%.1f%%).\n",
			formatBytes(report.TotalSizeBytes), report.TotalWorkspaceCount,
			formatBytes(report.TotalArtifactSizeBytes), report.TotalArtifactRatio*100)
	}
	printRepoCacheLine(w, report)
}

// printDiskUsageOtherRootsHint 提示：除刚扫的那个外，别的工作区根也放着任务目录。
// 只要存在这样的根就提示（当前根空不空都行）；唯一退路是显式 --workspaces-root——
// 用户已经亲手选定要扫什么。
func printDiskUsageOtherRootsHint(w io.Writer, report daemon.DiskUsageReport, profile, rootOverride string, taskContext bool) {
	if rootOverride != "" {
		return
	}
	// 建议靠列 ~/.multica/profiles 生成，所以任务里打印它们会泄露 Owner 的 profile
	// 名——正是拒掉 --all-profiles 所防的——而且它们建议的每条命令在任务里都会被拒。
	if taskContext {
		return
	}
	suggestions := diskUsageProfileSuggestions(profile, report.WorkspacesRoot)
	if len(suggestions) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Other workspace roots contain task directories:")
	for _, s := range suggestions {
		fmt.Fprintf(w, "  %s  # %s (%d task%s)\n",
			s.Command, s.Root, s.TaskCount, pluralS(s.TaskCount))
	}
	fmt.Fprintln(w, "Run 'multica daemon disk-usage --all-profiles' for a combined total across all roots.")
}

// diskUsageProfileSuggestion 是一条给用户的建议：某个别的 profile 根里有任务目录，
// 附上可直接跑的命令。
type diskUsageProfileSuggestion struct {
	Profile   string
	Command   string
	Root      string
	TaskCount int
}

// diskUsageProfileSuggestions 找出「还有别处占着任务目录」的根并生成建议命令，
// 最多 5 条、按任务数降序。只有当前跑的是命名 profile 时才补默认根一条。
func diskUsageProfileSuggestions(currentProfile, currentRoot string) []diskUsageProfileSuggestion {
	out := make([]diskUsageProfileSuggestion, 0)
	if currentProfile != "" {
		if root, err := resolveWorkspacesRootForProfile("", ""); err == nil && !samePath(root, currentRoot) {
			if taskCount := countDiskUsageTaskDirs(root); taskCount > 0 {
				out = append(out, diskUsageProfileSuggestion{
					Profile:   "",
					Command:   "multica daemon disk-usage",
					Root:      root,
					TaskCount: taskCount,
				})
			}
		}
	}

	profilesRoot, err := profilesRootDir()
	if err != nil {
		return out
	}
	entries, err := os.ReadDir(profilesRoot)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		profile := entry.Name()
		if profile == currentProfile {
			continue
		}
		root, err := resolveWorkspacesRootForProfile(profile, "")
		if err != nil || samePath(root, currentRoot) {
			continue
		}
		taskCount := countDiskUsageTaskDirs(root)
		if taskCount == 0 {
			continue
		}
		out = append(out, diskUsageProfileSuggestion{
			Profile:   profile,
			Command:   "multica --profile " + shellQuoteArg(profile) + " daemon disk-usage",
			Root:      root,
			TaskCount: taskCount,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TaskCount == out[j].TaskCount {
			return out[i].Profile < out[j].Profile
		}
		return out[i].TaskCount > out[j].TaskCount
	})
	const maxSuggestions = 5
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out
}

// shellQuoteArg 需要时给参数加单引号 shell 引用；只由安全字符组成就直接返回。
func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' ||
			r >= '0' && r <= '9' ||
			r >= 'A' && r <= 'Z' ||
			r >= 'a' && r <= 'z')
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// countDiskUsageTaskDirs 数 root 下第两级子目录里的任务目录数量（.repos 除外），
// 供「别的根还有任务」提示用。
func countDiskUsageTaskDirs(root string) int {
	wsEntries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	count := 0
	for _, wsEntry := range wsEntries {
		if !wsEntry.IsDir() || wsEntry.Name() == ".repos" {
			continue
		}
		taskEntries, err := os.ReadDir(filepath.Join(root, wsEntry.Name()))
		if err != nil {
			continue
		}
		for _, taskEntry := range taskEntries {
			if taskEntry.IsDir() {
				count++
			}
		}
	}
	return count
}

// profilesRootDir 返回 ~/.multica/profiles 目录路径。
func profilesRootDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".multica", "profiles"), nil
}

// samePath 判断两个路径是否指向同一目录（都转绝对路径后比较）。
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return aa == bb
}

// pluralS 按数量返回英文复数后缀 ""/"s"（提示文案里 "N task(s)"）。
func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formatRatio 把 0..1 分数渲染成一位小数的百分比。非有限或负数输入塌缩成 "0.0%"
// ——total=0 的工作区不该冒出 "NaN%"。
func formatRatio(r float64) string {
	if r != r || r < 0 { // 用不等式做 NaN 检查
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", r*100)
}

// emptyDash 空串渲染成 "-"，供表格占位。
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatBytes 按 IEC 单位（KiB/MiB/GiB）渲染字节数，1 KiB 以上保留一位小数。故意
// 保持紧凑，表格视图在终端宽度下才扫得动。
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	prefix := "KMGTPE"[exp]
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), prefix)
}

// formatAge 按最合适人读的单位渲染年龄，保持数值大于 1。"0s" 表示「不足一秒」——
// 与 GC 日志行的样式一致。
func formatAge(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int((d%(24*time.Hour))/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
