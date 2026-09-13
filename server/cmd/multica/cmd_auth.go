// 文件职责：实现 `multica auth` 命令（status/logout）与三处共享的登录逻辑：
// token 直登（runAuthLoginToken）、浏览器 OAuth 回环登（runAuthLoginBrowser，
// 在本机起回调服务器接 JWT 再换 PAT）、token/AppURL 解析。browser 流里「回调地址
// 指向哪台机器」是易错点，resolveCallbackBinding 单独抽出并做单元测试。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/cli"
)

// loginTokenPrefixes 是 `multica login --token` 接受的前缀。CLI 曾只写死 `mul_`，
// 结果就算 server 两种都认，也没法用 Multica Cloud Node 的 PAT（`mcn_`）登录。
// 保持与 server/internal/middleware/auth.go 里的前缀分支同步。
var loginTokenPrefixes = []string{"mul_", auth.CloudPATPrefix}

// validateLoginTokenPrefix 校验 token 以某个 CLI 认可的前缀开头，否则返回描述
// 可接受集合的错误。抽出成函数让前缀列表有单一、显眼的测试面。
func validateLoginTokenPrefix(token string) error {
	for _, p := range loginTokenPrefixes {
		if strings.HasPrefix(token, p) {
			return nil
		}
	}
	return fmt.Errorf("invalid token format: must start with %s", strings.Join(loginTokenPrefixes, " or "))
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate multica with Multica",
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current authentication status",
	RunE:  runAuthStatus,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored authentication token",
	RunE:  runAuthLogout,
}

// callbackHostFlag 让用户覆盖进入 OAuth cli_callback URL 的主机/IP。CLI 在反向
// 代理后、或自动探测出的局域网 IP 不是浏览器能访问的那个时很管用。
const callbackHostFlag = "callback-host"

// callbackHostFlagHelp 是 --callback-host 的帮助文案（与命令一起注册）。
const callbackHostFlagHelp = "Host/IP the OAuth callback URL points at when the browser can reach this CLI directly. For SSH-only machines, use the printed tunnel hint instead."

func init() {
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)
}

// resolveToken 按优先级取当前有效 token：MULTICA_TOKEN 环境变量 > 用户配置。
func resolveToken(cmd *cobra.Command) string {
	if v := strings.TrimSpace(os.Getenv("MULTICA_TOKEN")); v != "" {
		return v
	}
	// daemon 管理的任务内部绝不能静默回退到用户全局配置 token：正是这种静默回退
	// 会让 agent 的写入落到错误的执行者头上。inDaemonManagedExecutionContext 已
	// 覆盖丢失 MULTICA_AGENT_ID / MULTICA_TASK_ID 的子进程的 MULTICA_DAEMON_PORT
	// 信号。
	if inDaemonManagedExecutionContext() {
		return ""
	}
	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	return cfg.Token
}

// resolveAppURL 解析 App 前端 URL（环境变量优先，其次配置）。取不到时打印引导并
// os.Exit(1)——浏览器登录流程没有它就走不下去。
func resolveAppURL(cmd *cobra.Command) string {
	for _, key := range []string{"MULTICA_APP_URL", "FRONTEND_ORIGIN"} {
		if val := strings.TrimSpace(os.Getenv(key)); val != "" {
			return strings.TrimRight(val, "/")
		}
	}
	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err == nil && cfg.AppURL != "" {
		return strings.TrimRight(cfg.AppURL, "/")
	}
	fmt.Fprintln(os.Stderr, "No app URL configured. Run 'multica setup' first.")
	os.Exit(1)
	return "" // 不可达（上面已 os.Exit）
}

// openBrowser 用平台默认浏览器打开 URL（darwin: open / linux: xdg-open /
// windows: rundll32）。
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return exec.Command(cmd, args...).Start()
}

// runAuthLogin 是 `login`/`auth login` 的入口分发：传了 --token 就走 token 直登，
// 否则走浏览器 OAuth。
func runAuthLogin(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("login"); err != nil {
		return err
	}
	if cmd.Flags().Changed("token") {
		tokenFlag, _ := cmd.Flags().GetString("token")
		// `--token mul_xxx`（空格分隔）是用户真实会敲的形态——文档和 #1994 里的
		// 写法。NoOptDefVal 阻止 pflag 把下一个参数当 flag 值消费，于是它作为位置
		// 参数落到这里。提升为 token 值。
		if tokenFlag == tokenPromptSentinel && len(args) == 1 {
			tokenFlag = args[0]
		}
		return runAuthLoginToken(cmd, tokenFlag)
	}
	return runAuthLoginBrowser(cmd)
}

// resolveCallbackBinding 选出写进 `cli_callback` URL 的主机，以及 CLI 本地 HTTP
// 监听器绑定的网卡地址。
//
// 跑登录流程的浏览器在 *server* 的机器上（或用户点开链接的地方），不在 CLI
// 主机上。因此回调 URL 必须解析成浏览器真能访问的地址，各拓扑下不同：
//
//   - hosted / 公网 app URL：浏览器与 CLI 同机，localhost 即可。
//   - 自托管、CLI 在 server 主机上：同上。
//   - 自托管、CLI 在另一台局域网机器：回调 URL 必须指向 CLI 自己的局域网 IP，
//     而不是 server 的。
//   - 反代 / FQDN 场景：自动探测不知道正确主机——由用户经 --callback-host 提供。
//
// detectOutbound 是注入的，让测试不发起真实网络调用也能覆盖路由决策。
func resolveCallbackBinding(flagHost, serverURL, appURL string, detectOutbound func(string) net.IP) (callbackHost, bindAddr string) {
	// 显式 flag 永远优先。绑定所有网卡，无论主机名落到哪块网卡浏览器都能访问到。
	if h := strings.TrimSpace(flagHost); h != "" {
		return h, "0.0.0.0"
	}

	appIP := urlPrivateIP(appURL)
	if appIP == nil {
		// 公网主机名、没有私有 IP 映射的 FQDN，或解析失败。回环是唯一安全默认——
		// hosted/公网部署里浏览器与 CLI 同机。
		return "localhost", "127.0.0.1"
	}

	// app_url 是私有局域网 IP。向内核询问去往 server 时会用哪个本地地址，据此判断
	// CLI 与该 server 同机还是异机。同机 → 回环即可；异机 → 用 CLI 的出站 IP，
	// 让浏览器能找到我们。
	cliIP := detectOutbound(serverURL)
	if cliIP == nil {
		// 探测失败（离线、server 不可达等）。回退到 app IP——保持先前同机部署的行为。
		return appIP.String(), "0.0.0.0"
	}
	if cliIP.Equal(appIP) {
		return "localhost", "127.0.0.1"
	}
	return cliIP.String(), "0.0.0.0"
}

// urlPrivateIP 把 rawURL 解析后取出主机名；只有它是 RFC 1918 私有字面 IP 才返回，
// 否则（URL 解析失败或主机不是私有 IP）返回 nil。
func urlPrivateIP(rawURL string) net.IP {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsPrivate() {
		return nil
	}
	return ip
}

// detectOutboundIP 返回 OS 去往 serverURL 时会用的本地 IPv4，探测失败返回 nil。
// UDP dial 并不发包——只是让内核为这条目的路由挑一个源 IP。
func detectOutboundIP(serverURL string) net.IP {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Hostname() == "" {
		return nil
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	conn, err := net.Dial("udp4", net.JoinHostPort(parsed.Hostname(), port))
	if err != nil {
		return nil
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		return nil
	}
	// 归一成 4 字节形态，让 Equal() 与 net.ParseIP 的输出一致可比。
	if v4 := local.IP.To4(); v4 != nil {
		return v4
	}
	return local.IP
}

// runAuthLoginBrowser 走浏览器 OAuth：在本机起一个仅回调用的 HTTP 服务，带 CSRF
// state 的登录 URL 打开浏览器后等待 JWT 回调，随后用它换 PAT 并保存到配置。
func runAuthLoginBrowser(cmd *cobra.Command) error {
	serverURL := resolveHumanServerURL(cmd)
	appURL := resolveAppURL(cmd)

	flagHost := callbackHostFlagValue(cmd)
	callbackHost, bindAddr := resolveCallbackBinding(flagHost, serverURL, appURL, detectOutboundIP)

	// 钉到 "tcp4"：macOS 上裸 "tcp" 可能产出纯 IPv6 的 socket，IPv4 客户端
	// （包括把 localhost 解析成 127.0.0.1 的浏览器）连不上。回调 URL 永远是 IPv4
	// 字面量或主机名，IPv4 监听器正是浏览器需要的。
	listener, err := net.Listen("tcp4", bindAddr+":0")
	if err != nil {
		return fmt.Errorf("could not start the local login callback server (used to receive the browser sign-in); a firewall or another process may be blocking local ports: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://%s:%d/callback", callbackHost, port)

	// 为 CSRF 防护生成随机 state 参数。
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return fmt.Errorf("failed to generate state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	loginURL := fmt.Sprintf("%s/login?cli_callback=%s&cli_state=%s", appURL, url.QueryEscape(callbackURL), url.QueryEscape(state))

	// 接收浏览器回调 JWT 的通道。
	jwtCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		returnedState := r.URL.Query().Get("state")
		if returnedState != state {
			http.Error(w, "invalid state parameter", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(callbackSuccessHTML))
		jwtCh <- token
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	defer srv.Close()

	// 打开浏览器。
	fmt.Fprintln(os.Stderr, "Opening browser to authenticate...")
	if err := openBrowser(loginURL); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser automatically.\n")
	}
	fmt.Fprint(os.Stderr, browserLoginInstructions(loginURL, callbackHost, port, runningInSSHSession()))

	// 等回调带回 JWT（5 分钟超时）。
	var jwtToken string
	select {
	case jwtToken = <-jwtCh:
	case err := <-errCh:
		return fmt.Errorf("local server error: %w", err)
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("timed out waiting for authentication")
	}

	// 用 JWT 走既有 API 换一个 PAT。
	client := cli.NewAPIClient(serverURL, "", jwtToken)

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	patName := fmt.Sprintf("CLI (%s)", hostname)
	expiresInDays := 90

	var patResp struct {
		Token string `json:"token"`
	}
	err = client.PostJSON(ctx, "/api/tokens", map[string]any{
		"name":            patName,
		"expires_in_days": expiresInDays,
	}, &patResp)
	if err != nil {
		return cli.WithUserMessage("Sign-in did not complete: the server could not issue an access token for the CLI. Run `multica login` again.", err)
	}

	// 验证 PAT 可用。
	patClient := cli.NewAPIClient(serverURL, "", patResp.Token)
	var me struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := patClient.GetJSON(ctx, "/api/me", &me); err != nil {
		return cli.WithUserMessage("Sign-in did not complete: the server did not accept the new credential. Run `multica login` again.", err)
	}

	// 保存进配置。每次登录都清掉工作区数据——用户或 server 可能已变，过期的工作区
	// 不该残留。
	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	cfg.WorkspaceID = ""
	cfg.Token = patResp.Token
	cfg.ServerURL = serverURL
	cfg.AppURL = appURL
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Authenticated as %s (%s)\nToken saved to config.\n", me.Name, me.Email)
	return nil
}

// runningInSSHSession 判断当前是否在 SSH 会话里（用于给用户加端口转发提示）。
func runningInSSHSession() bool {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// callbackHostFlagValue 从命令链（含父命令与继承 flag）里找 --callback-host 的值。
func callbackHostFlagValue(cmd *cobra.Command) string {
	for c := cmd; c != nil; c = c.Parent() {
		if value := nonEmptyFlagValue(c.Flags(), callbackHostFlag); value != "" {
			return value
		}
		if value := nonEmptyFlagValue(c.PersistentFlags(), callbackHostFlag); value != "" {
			return value
		}
		if value := nonEmptyFlagValue(c.InheritedFlags(), callbackHostFlag); value != "" {
			return value
		}
	}
	return ""
}

// nonEmptyFlagValue 取 flag 的非空字符串值。
func nonEmptyFlagValue(flags *pflag.FlagSet, name string) string {
	if flag := flags.Lookup(name); flag != nil {
		return strings.TrimSpace(flag.Value.String())
	}
	return ""
}

// callbackHostIsLoopback 判断回调主机是否指向回环（localhost 或回环 IP）。
func callbackHostIsLoopback(host string) bool {
	h := strings.Trim(strings.TrimSpace(host), "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// browserLoginInstructions 拼浏览器登录的指引文本：URL 之外，SSH 会话且回调是
// 回环时附上 `ssh -L` 端口转发命令，否则浏览器根本够不到 CLI 的回调端口。
func browserLoginInstructions(loginURL, callbackHost string, port int, remoteSSH bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "If the browser didn't open, visit:\n  %s\n", loginURL)
	if remoteSSH && callbackHostIsLoopback(callbackHost) {
		fmt.Fprintf(&b, "\nRemote SSH session detected. Before opening that URL on your local computer, forward the callback port in another terminal:\n  ssh -L %d:127.0.0.1:%d <user>@<remote-host>\nThen open the URL above in your local browser.\n", port, port)
	}
	fmt.Fprintln(&b, "\nWaiting for authentication...")
	return b.String()
}

// runAuthLoginToken 走 token 直登：providedToken 为空（或等于 prompt sentinel）时
// 交互式读取；前缀不合法即拒；用 token 验证 /api/me 后写入配置。
func runAuthLoginToken(cmd *cobra.Command, providedToken string) error {
	// prompt sentinel 是 pflag 给无值的 `--token` 换上的东西（见 loginCmd init）；
	// 把它当空串处理，落到下面的交互提示。
	if providedToken == tokenPromptSentinel {
		providedToken = ""
	}
	token := strings.TrimSpace(providedToken)
	if token == "" {
		fmt.Print("Enter your personal access token: ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return fmt.Errorf("no input")
		}
		token = strings.TrimSpace(scanner.Text())
	}
	if token == "" {
		return fmt.Errorf("token is required")
	}
	if err := validateLoginTokenPrefix(token); err != nil {
		return err
	}

	serverURL := resolveLoginTokenServerURL(cmd)
	client := cli.NewAPIClient(serverURL, "", token)

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var me struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := client.GetJSON(ctx, "/api/me", &me); err != nil {
		return cli.WithUserMessage("Could not sign in with that token — make sure it is valid and not expired, then run `multica login --token <token>` again.", err)
	}

	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	cfg.WorkspaceID = ""
	cfg.Token = token
	cfg.ServerURL = serverURL
	if cfg.AppURL == "" && serverURL == defaultCloudServerURL {
		cfg.AppURL = defaultCloudAppURL
	}
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Authenticated as %s (%s)\nToken saved to config.\n", me.Name, me.Email)
	return nil
}

// runAuthStatus 执行 `auth status`：展示登录状态。任务上下文强制要求 mat_ 前缀的
// 任务级 token；未登录或 token 失效都给明确的下一步指引（不返回错误——「未认证」
// 本身是合法状态）。
func runAuthStatus(cmd *cobra.Command, _ []string) error {
	if err := requireTaskLocalConfigRoot(); err != nil {
		return err
	}
	taskContext := inDaemonManagedExecutionContext()
	token := resolveToken(cmd)
	if taskContext && !strings.HasPrefix(token, "mat_") {
		return fmt.Errorf("agent execution context requires MULTICA_TOKEN to be a task-scoped mat_ token")
	}
	serverURL := resolveServerURL(cmd)

	if token == "" {
		fmt.Fprintln(os.Stderr, "Not authenticated. Run 'multica login' to authenticate.")
		return nil
	}

	client := cli.NewAPIClient(serverURL, "", token)

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var me struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := client.GetJSON(ctx, "/api/me", &me); err != nil {
		fmt.Fprintf(os.Stderr, "Token is invalid or expired: %v\nRun 'multica login' to re-authenticate.\n", err)
		return nil
	}

	if taskContext {
		fmt.Fprintf(os.Stderr, "Server:  %s\nUser:    %s (%s)\n", serverURL, me.Name, me.Email)
		return nil
	}

	prefix := token
	if len(prefix) > 12 {
		prefix = prefix[:12] + "..."
	}
	fmt.Fprintf(os.Stderr, "Server:  %s\nUser:    %s (%s)\nToken:   %s\n", serverURL, me.Name, me.Email, prefix)
	return nil
}

const callbackSuccessHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Multica — Authenticated</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #0b0b0f; --card-bg: #16161d; --border: rgba(255,255,255,0.10); --fg: #f5f5f5; --fg2: #a1a1aa; --accent: #22c55e; --accent-bg: rgba(34,197,94,0.12); }
  }
  @media (prefers-color-scheme: light) {
    :root { --bg: #f8f8fa; --card-bg: #ffffff; --border: rgba(0,0,0,0.08); --fg: #0f0f12; --fg2: #71717a; --accent: #16a34a; --accent-bg: rgba(22,163,74,0.08); }
  }
  body { font-family: -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; background: var(--bg); color: var(--fg); display: flex; align-items: center; justify-content: center; min-height: 100vh; }
  .card { width: 100%; max-width: 380px; border: 1px solid var(--border); border-radius: 12px; background: var(--card-bg); padding: 40px 32px; text-align: center; }
  .icon-wrap { width: 48px; height: 48px; margin: 0 auto 24px; background: var(--accent-bg); border-radius: 50%; display: flex; align-items: center; justify-content: center; }
  .icon-wrap svg { width: 24px; height: 24px; color: var(--accent); }
  .brand { display: flex; align-items: center; justify-content: center; gap: 6px; margin-bottom: 8px; }
  .asterisk { display: inline-block; width: 14px; height: 14px; background: var(--fg); clip-path: polygon(45% 62.1%,45% 100%,55% 100%,55% 62.1%,81.8% 88.9%,88.9% 81.8%,62.1% 55%,100% 55%,100% 45%,62.1% 45%,88.9% 18.2%,81.8% 11.1%,55% 37.9%,55% 0%,45% 0%,45% 37.9%,18.2% 11.1%,11.1% 18.2%,37.9% 45%,0% 45%,0% 55%,37.9% 55%,11.1% 81.8%,18.2% 88.9%); }
  h1 { font-size: 20px; font-weight: 600; margin-bottom: 8px; }
  p { font-size: 14px; color: var(--fg2); line-height: 1.5; }
  .hint { margin-top: 24px; font-size: 13px; color: var(--fg2); opacity: 0.7; }
</style>
</head>
<body>
  <div class="card">
    <div class="icon-wrap">
      <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5"/></svg>
    </div>
    <div class="brand"><span class="asterisk"></span></div>
    <h1>Authentication successful</h1>
    <p>You can close this tab and return to the terminal.</p>
    <p class="hint">Your CLI session is now authenticated.</p>
  </div>
  <script>setTimeout(function(){window.close()},3000)</script>
</body>
</html>`

// runAuthLogout 执行 `auth logout`：清掉本机配置里的 token。
func runAuthLogout(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("logout"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "Not authenticated.")
		return nil
	}

	cfg.Token = ""
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Token removed. You are now logged out.")
	return nil
}
