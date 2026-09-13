// 文件职责：实现 `multica setup` 命令——一键引导：写 CLI 配置（Cloud 或
// self-host）、浏览器登录、启动 daemon。self-host 分支里「写配置前先探测可达性」
// 与「URL 解析回退链」是重点（避免失败的 setup 冲掉可用配置或把远程部署悄悄重置
// 回 localhost）。
package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// setupCmd 是 `multica setup` 命令（默认连 Multica Cloud），子命令：cloud /
// self-host，流程与 SSH/--callback-host 提示详见其 Long 文本。
var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure the CLI, authenticate, and start the daemon",
	Long: `Configures the CLI to connect to Multica Cloud (multica.ai), then
authenticates via browser and starts the agent daemon.

If a configuration already exists, you will be prompted before overwriting.

Use 'multica setup self-host' to connect to a self-hosted server instead.

If you run this command over SSH on a remote machine, keep the localhost
callback and follow the SSH tunnel hint printed during browser login. If your
browser can reach this CLI directly on a private network address, pass
--callback-host <host-or-ip>.

Use --profile to create an isolated configuration for a separate environment:
  multica setup self-host --profile staging --server-url https://api-staging.co`,
	RunE: runSetupCloud,
}

var setupCloudCmd = &cobra.Command{
	Use:   "cloud",
	Short: "Configure the CLI for Multica Cloud (multica.ai)",
	Long: `Explicitly configures the CLI to connect to Multica Cloud (multica.ai).

If you run this command over SSH on a remote machine, keep the localhost
callback and follow the SSH tunnel hint printed during browser login. If your
browser can reach this CLI directly on a private network address, pass
--callback-host <host-or-ip>.

This is equivalent to running 'multica setup' without a subcommand.`,
	RunE: runSetupCloud,
}

// setupSelfHostCmd 配置连自托管 server。
var setupSelfHostCmd = &cobra.Command{
	Use:   "self-host",
	Short: "Configure the CLI for a self-hosted Multica server",
	Long: `Configures the CLI to connect to a self-hosted Multica server.

By default, connects to http://localhost:8080 (backend) and http://localhost:3000 (frontend).
Use --server-url and --app-url to specify a custom server (e.g. an on-premise deployment).

If you run this command from a different machine than the server, also pass
--callback-host <host-or-ip-the-browser-can-reach-back-to-this-machine-on> so
the OAuth login flow can return the token to the CLI.

Examples:
  multica setup self-host
  multica setup self-host --server-url https://api.internal.co --app-url https://app.internal.co
  multica setup self-host --port 9090 --frontend-port 4000`,
	RunE: runSetupSelfHost,
}

func init() {
	setupCmd.Flags().String(callbackHostFlag, "", callbackHostFlagHelp)
	setupCloudCmd.Flags().String(callbackHostFlag, "", callbackHostFlagHelp)

	setupSelfHostCmd.Flags().String("server-url", "", "Backend server URL (e.g. https://api.internal.co) (env: MULTICA_SERVER_URL)")
	setupSelfHostCmd.Flags().String("app-url", "", "Frontend app URL (e.g. https://app.internal.co) (env: MULTICA_APP_URL)")
	setupSelfHostCmd.Flags().Int("port", 8080, "Backend server port (used when --server-url is not set)")
	setupSelfHostCmd.Flags().Int("frontend-port", 3000, "Frontend port (used when --app-url is not set)")
	setupSelfHostCmd.Flags().String(callbackHostFlag, "", callbackHostFlagHelp)

	setupCmd.AddCommand(setupCloudCmd)
	setupCmd.AddCommand(setupSelfHostCmd)
}

// printConfigLocation 打印配置文件的路径（与 profile 名）。
func printConfigLocation(profile string) {
	path, err := cli.CLIConfigPathForProfile(profile)
	if err != nil {
		return
	}
	if profile != "" {
		fmt.Fprintf(os.Stderr, "  profile:    %s\n", profile)
	}
	fmt.Fprintf(os.Stderr, "  config:     %s\n", path)
}

// confirmOverwrite 检查既有配置，覆盖前先问用户。newServerURL/newAppURL 是 setup
// 即将写入的值，与现值不同时按 "old -> new" 展示，让用户看到传入的 flag/env 已被
// 接收而非悄悄忽略。返回 true 表示继续、false 表示用户拒绝。
func confirmOverwrite(profile, newServerURL, newAppURL string) (bool, error) {
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return true, nil // 读不了配置 → 当作还没有配置
	}
	if cfg.ServerURL == "" {
		return true, nil // 没配过 server → 全新配置
	}

	fmt.Fprintln(os.Stderr, "Current configuration:")
	fmt.Fprintf(os.Stderr, "  server_url: %s\n", formatURLChange(cfg.ServerURL, newServerURL))
	fmt.Fprintf(os.Stderr, "  app_url:    %s\n", formatURLChange(cfg.AppURL, newAppURL))
	if cfg.WorkspaceID != "" {
		fmt.Fprintf(os.Stderr, "  workspace:  %s\n", cfg.WorkspaceID)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprint(os.Stderr, "This will reset your configuration. Continue? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		fmt.Fprintln(os.Stderr, "Aborted.")
		return false, nil
	}
	return true, nil
}

// formatURLChange 在 setup 即将改动值时渲染 "old -> new"，不变时只渲染当前值。
func formatURLChange(oldVal, newVal string) string {
	if newVal != "" && newVal != oldVal {
		return fmt.Sprintf("%s  ->  %s", oldVal, newVal)
	}
	return oldVal
}

// runSetupCloud 执行 `setup`（或 `setup cloud`）：把配置指向 Multica Cloud、
// 确认覆盖后保存，随后登录并启动 daemon。
func runSetupCloud(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("setup"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)

	cfg := cli.CLIConfig{
		ServerURL: defaultCloudServerURL,
		AppURL:    defaultCloudAppURL,
	}

	ok, err := confirmOverwrite(profile, cfg.ServerURL, cfg.AppURL)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Configured for Multica Cloud (https://multica.ai).")
	fmt.Fprintf(os.Stderr, "  server_url: %s\n", cfg.ServerURL)
	fmt.Fprintf(os.Stderr, "  app_url:    %s\n", cfg.AppURL)
	printConfigLocation(profile)

	// 进入登录流程。
	fmt.Fprintln(os.Stderr, "")
	if err := runLogin(cmd, args); err != nil {
		return err
	}

	if err := runDaemonAfterSetup(cmd, args); err != nil {
		return fmt.Errorf("start or restart daemon: %w", err)
	}
	fmt.Fprintln(os.Stderr, "\n✓ Setup complete! Your machine is now connected to Multica.")

	return nil
}

// runSetupSelfHost 执行 `setup self-host`：解析后端/前端 URL（见 resolve* 的
// 回退链），必要时让用户填前端 URL，确认覆盖后先探测 server 可达性再落盘——
// 不可达则保留旧配置（含登录态）。
func runSetupSelfHost(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("setup"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)

	// 在确认覆盖前先解析目标 URL，提示里才能展示将写入的值（"old -> new"），
	// 让用户确认传入的 flag 生效了。
	//
	// 对应 flag 未设时优先读 MULTICA_SERVER_URL / MULTICA_APP_URL——与 CLI 其余
	// 部分（resolveServerURL）及根 --server-url flag 上文档化的环境变量一致。
	// 在这之前 setup self-host 只认 flag，于是设了 MULTICA_SERVER_URL 的自托管
	// 用户仍然拿到 localhost 默认值和一句 "unreachable" 报错（GitHub #3912）。
	existing, _ := cli.LoadCLIConfigForProfile(profile)
	serverURL, userProvidedServerURL := resolveSelfHostServerURL(cmd, existing)
	appURL := resolveSelfHostAppURL(cmd, existing)
	frontendPort, _ := cmd.Flags().GetInt("frontend-port")

	if appURL == "" {
		if userProvidedServerURL && !serverHostIsLocal(serverURL) {
			// 远程 server 猜不出前端 URL：api.x.co 与 app.x.co，或 https 前置的
			// 部署，硬猜只会静默产生一条坏的登录 URL。改为问用户。
			entered, err := promptAppURL(serverURL)
			if err != nil {
				return err
			}
			if entered == "" {
				return fmt.Errorf("--app-url is required when --server-url points at a remote host (e.g. --app-url https://app.internal.co)")
			}
			appURL = entered
		} else {
			appURL = fmt.Sprintf("http://localhost:%d", frontendPort)
		}
	}

	ok, err := confirmOverwrite(profile, serverURL, appURL)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	// 落盘前先探测。失败的 setup 绝不能覆盖可用配置或抹掉已存 token：
	// persistSelfHostConfigIfReachable 只在 server 应答时才写，host 不可达时旧配置
	// 原样保留、用户保持登录。（原本的顺序是先存再探测再退出——每次失败的探测
	// 都会抹掉 token。）
	reachable, err := persistSelfHostConfigIfReachable(serverURL, appURL, profile, probeServer)
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if !reachable {
		fmt.Fprintf(os.Stderr, "\n⚠ Server at %s is not reachable.\n", serverURL)
		fmt.Fprintln(os.Stderr, "  Your existing configuration was left unchanged.")
		fmt.Fprintln(os.Stderr, "  Verify the URL, then re-run 'multica setup self-host' once it's reachable.")
		return nil
	}

	fmt.Fprintln(os.Stderr, "Configured for self-hosted server.")
	fmt.Fprintf(os.Stderr, "  server_url: %s\n", serverURL)
	fmt.Fprintf(os.Stderr, "  app_url:    %s\n", appURL)
	printConfigLocation(profile)

	// 进入登录流程。
	fmt.Fprintln(os.Stderr, "")
	if err := runLogin(cmd, args); err != nil {
		return err
	}

	if err := runDaemonAfterSetup(cmd, args); err != nil {
		return fmt.Errorf("start or restart daemon: %w", err)
	}
	fmt.Fprintln(os.Stderr, "\n✓ Setup complete! Your machine is now connected to Multica.")

	return nil
}

// runDaemonAfterSetup 让新登录的 profile 在 daemon 进程里生效。daemon 只在启动时
// 读 server URL 与 token，所以已有 daemon 在跑时只覆盖 config.json 没用——它还连
// 着旧部署。需要时重启该 profile，否则做正常的首次启动。
func runDaemonAfterSetup(cmd *cobra.Command, args []string) error {
	profile := resolveProfile(cmd)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	health := checkDaemonHealthOnPort(ctx, healthPortForProfile(profile))

	return dispatchDaemonAfterSetup(
		cmd,
		args,
		health,
		func(cmd *cobra.Command, _ []string) error {
			return runDaemonBackground(cmd)
		},
		runDaemonRestart,
	)
}

// setupDaemonRunner 是「启动/重启 daemon」的可注入函数（测试换假实现用）。
type setupDaemonRunner func(*cobra.Command, []string) error

// dispatchDaemonAfterSetup 决定 daemon 的处理：健康且在跑时，若还有活跃任务就
// 提醒用户手动重启（避免取消进行中的工作）；无活跃任务则重启以应用新配置；没在
// 跑就首次启动。
func dispatchDaemonAfterSetup(
	cmd *cobra.Command,
	args []string,
	health map[string]any,
	start setupDaemonRunner,
	restart setupDaemonRunner,
) error {
	if daemonAlive(health) {
		if activeTasks := daemonActiveTaskCount(health); activeTasks > 0 {
			taskLabel := "tasks"
			if activeTasks == 1 {
				taskLabel = "task"
			}
			restartCmd := "multica daemon restart"
			if profile := resolveProfile(cmd); profile != "" {
				restartCmd += " --profile " + profile
			}
			return fmt.Errorf(
				"daemon has %d active %s; setup saved the new configuration but left the running daemon unchanged to avoid cancelling work. Wait for the active work to finish, then run '%s' to apply the new configuration",
				activeTasks,
				taskLabel,
				restartCmd,
			)
		}
		fmt.Fprintln(os.Stderr, "\nRestarting daemon to apply the new configuration...")
		return restart(cmd, args)
	}

	fmt.Fprintln(os.Stderr, "\nStarting daemon...")
	return start(cmd, args)
}

// daemonActiveTaskCount 从健康检查响应里取活跃任务数（兼容 JSON 的 float64 形态）。
func daemonActiveTaskCount(health map[string]any) int64 {
	switch count := health["active_task_count"].(type) {
	case float64:
		return int64(count)
	case int:
		return int64(count)
	case int64:
		return count
	default:
		return 0
	}
}

// persistSelfHostConfigIfReachable 先探测 serverURL，只有它应答时才用给定
// self-host URL 覆盖 profile 配置。不可达时保留既有配置与 token、返回 false——
// 失败的 `setup self-host` 既不登出用户也不冲掉可用配置（最初是「先存后探测再
// 退出」，每次失败的探测都抹掉 token）。prober 注入便于测试不走真实网络。
func persistSelfHostConfigIfReachable(serverURL, appURL, profile string, probe func(string) bool) (bool, error) {
	if !probe(serverURL) {
		return false, nil
	}
	cfg := cli.CLIConfig{
		ServerURL: serverURL,
		AppURL:    appURL,
	}
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return false, err
	}
	return true, nil
}

// resolveSelfHostServerURL 为 `setup self-host` 选后端 URL：--server-url flag 优先，
// 然后 MULTICA_SERVER_URL 环境变量（与 CLI 其余部分及根 flag 上文档化的一致），
// 再是既有配置里的 server_url，最后是 --port 拼的 localhost 默认。userProvided 为
// true 表示 URL 来自用户（flag/env/既有配置）而非 localhost 回退——调用方据此
// 判断远程 host 是否需要显式 app_url。
//
// 回退到 existing.ServerURL，意味着重跑 setup self-host（例如重新登录或重启
// daemon）会保留已配置的远程部署，而不是悄悄重置回 http://localhost:8080。
// 显式 --port 则回到 localhost 路径，适配本地开发场景。
//
// 用户给的 URL 会走 normalizeAPIBaseURL——与 resolveServerURL 同一条路径：
// MULTICA_SERVER_URL 文档化为 ws:// daemon 地址（如 ws://localhost:8080/ws），
// 所以 ws/wss 形态与尾缀 /ws 都被接受并转成可达性探测与存储 server_url 所期待的
// http(s) base。
func resolveSelfHostServerURL(cmd *cobra.Command, existing cli.CLIConfig) (serverURL string, userProvided bool) {
	if v := cli.FlagOrEnv(cmd, "server-url", "MULTICA_SERVER_URL", ""); v != "" {
		return normalizeAPIBaseURL(v), true
	}
	if !cmd.Flags().Changed("port") && existing.ServerURL != "" {
		// `config set server_url` 原样存值，可能是文档化的 ws:// daemon 形态；
		// 规整成探测与存储 server_url 期望的 http(s) base，与 resolveServerURL
		// 一致。
		return normalizeAPIBaseURL(existing.ServerURL), true
	}
	port, _ := cmd.Flags().GetInt("port")
	return fmt.Sprintf("http://localhost:%d", port), false
}

// resolveSelfHostAppURL 解析 `setup self-host` 的前端 URL：--app-url flag 优先，
// 然后 MULTICA_APP_URL，再是既有配置里的 app_url（除非传了 --frontend-port）。
// 全都没有时返回 ""，由调用方推断——远程 host 询问用户，否则回退
// localhost:<frontend-port>。
//
// 与 resolveSelfHostServerURL 镜像：重跑 setup self-host 会保留已配置的远程前端
// 而不是重置回 localhost。app_url 是普通前端 URL（不是 ws:// daemon 地址），
// 所以直接用、不过 normalizeAPIBaseURL。
func resolveSelfHostAppURL(cmd *cobra.Command, existing cli.CLIConfig) string {
	if v := cli.FlagOrEnv(cmd, "app-url", "MULTICA_APP_URL", ""); v != "" {
		return v
	}
	if !cmd.Flags().Changed("frontend-port") && existing.AppURL != "" {
		return existing.AppURL
	}
	return ""
}

// serverHostIsLocal 判断 serverURL 是否指向 CLI 所在机器（回环字面量或
// "localhost"）。用于决定 app_url 是照 server_url 推断还是用本地开发默认值。
func serverHostIsLocal(serverURL string) bool {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	h := parsed.Hostname()
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// promptAppURL 交互式询问前端 URL。远程 server_url 推不出前端 URL——
// api.example.com ≠ app.example.com（多数生产环境）——硬猜只会把失败推迟到浏览器
// 登录那一步。用户直接回车时返回空串。
func promptAppURL(serverURL string) (string, error) {
	fmt.Fprintf(os.Stderr, "No --app-url provided, and --server-url (%s) is remote.\n", serverURL)
	fmt.Fprint(os.Stderr, "Enter the frontend app URL (e.g. https://app.internal.co): ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", nil
	}
	return strings.TrimRight(strings.TrimSpace(line), "/"), nil
}

// probeServer 探测给定 URL 的 Multica 后端是否可达（GET /health 返回 200）。
func probeServer(baseURL string) bool {
	url := strings.TrimRight(baseURL, "/") + "/health"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
