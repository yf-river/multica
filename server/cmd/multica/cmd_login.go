// 文件职责：实现 `multica login` 命令——走标准 auth 登录流程（见 cmd_auth.go 的
// runAuthLogin），登录成功后自动发现并 watch 用户全部工作区。若当前账号还没有
// workspace，会尝试打开浏览器引导创建并轮询等待（见 waitForWorkspaceCreation）。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// tryResolveAppURL 返回配置里的 app URL，取不到返回 ""。与 resolveAppURL 不同，
// 它绝不调用 os.Exit。
func tryResolveAppURL(cmd *cobra.Command) string {
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
	return ""
}

// loginCmd 是 `multica login` 命令。
var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate and set up workspaces",
	Long:  "Log in to Multica, then automatically discover and watch all your workspaces.",
	// 允许最多一个位置参数，这样 `--token mul_...` / `--token mcn_...`（空格分隔
	// 形式）虽然 pflag 不绑定值，runAuthLogin 也能借它恢复出 token。
	Args: cobra.MaximumNArgs(1),
	RunE: runLogin,
}

// tokenPromptSentinel 是 pflag 在 `--token` 无显式值时所赋的值。runAuthLoginToken
// 把它当作「交互式提示我输入」，从而在保留老式 `multica login --token`（不带值）
// 形态的同时，也支持文档化的 `--token mul_...` / `--token mcn_...`（带值）形态。
//
// sentinel 必须是可打印字符：pflag 会把 NoOptDefVal 原样渲染进帮助输出
// （"--token string[=\"prompt\"]"），而 "\x00" 在内部被当作列对齐标记，NUL 前缀
// 的 sentinel 会破坏 `login -h`。与用户真的敲 `--token prompt` 撞车也无害——
// 那个字符串从来不是合法 PAT，交互式提示也算合理应对。
const tokenPromptSentinel = "prompt"

func init() {
	// usage 串里别放反引号：pflag 的 UnquoteUsage 把第一段反引号内容当作该 flag
	// 在帮助输出里的取值占位符。
	loginCmd.Flags().String("token", "", "Authenticate using a personal access token (mul_... user PAT or mcn_... Cloud Node PAT). Pass --token mul_... / --token mcn_... to supply it inline, or --token alone to be prompted interactively.")
	// NoOptDefVal 让 `--token`（不带值）保留老式提示输入的行为，同时
	// `--token mul_...` / `--token mcn_...` 及 `=value` 形式照常取值。
	loginCmd.Flags().Lookup("token").NoOptDefVal = tokenPromptSentinel
	loginCmd.Flags().String(callbackHostFlag, "", callbackHostFlagHelp)
}

// runLogin 执行 `login`：先过 human/local 命令守卫，再走标准 auth 流程，成功后
// 自动 watch 全部工作区。autoWatchWorkspaces 失败不致命——提示手动设置即可。
func runLogin(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("login"); err != nil {
		return err
	}
	// 执行标准 auth 登录流程。
	if err := runAuthLogin(cmd, args); err != nil {
		return err
	}

	// 自动发现并 watch 所有工作区。
	if err := autoWatchWorkspaces(cmd); err != nil {
		fmt.Fprintf(os.Stderr, "\nCould not auto-configure workspaces: %v\n", err)
		fmt.Fprintf(os.Stderr, "Run 'multica workspace list' and 'multica workspace watch <id>' to set up manually.\n")
		return nil
	}

	fmt.Fprintf(os.Stderr, "\n→ Run 'multica daemon start' to start your local agent runtime.\n")
	return nil
}

// autoWatchWorkspaces 用刚登录的 profile 拉取工作区列表并设默认工作区。
func autoWatchWorkspaces(cmd *cobra.Command) error {
	// runLogin 已过 human/local 守卫并保存了新登录的 profile。这里直接读该
	// profile 而不是用那些对任务不友好的通用解析器——后者遇到孤立的
	// MULTICA_DAEMON_PORT 信号会失败关闭。
	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		return fmt.Errorf("not authenticated")
	}
	if cfg.ServerURL == "" {
		return fmt.Errorf("server URL not configured")
	}

	client := cli.NewAPIClient(normalizeAPIBaseURL(cfg.ServerURL), "", cfg.Token)
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var workspaces []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := client.GetJSON(ctx, "/api/workspaces", &workspaces); err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	if len(workspaces) == 0 {
		var err error
		workspaces, err = waitForWorkspaceCreation(cmd, client)
		if err != nil {
			return err
		}
		if len(workspaces) == 0 {
			fmt.Fprintln(os.Stderr, "\nNo workspaces found.")
			return nil
		}
	}

	// 没有工作区时设默认工作区为第一个。
	if cfg.WorkspaceID == "" {
		cfg.WorkspaceID = workspaces[0].ID
	}

	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\nFound %d workspace(s):\n", len(workspaces))
	for _, ws := range workspaces {
		marker := "  "
		if ws.ID == cfg.WorkspaceID {
			marker = "* "
		}
		fmt.Fprintf(os.Stderr, "%s%s (%s)\n", marker, ws.Name, ws.ID)
	}
	if len(workspaces) > 1 {
		fmt.Fprintln(os.Stderr, "\nUse 'multica workspace switch <id|slug>' to change the default workspace.")
	}

	return nil
}

// waitForWorkspaceCreation 打开网页版「创建工作区」页面并轮询等待用户建好，
// 返回新的工作区列表。
func waitForWorkspaceCreation(cmd *cobra.Command, client *cli.APIClient) ([]struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}, error) {
	appURL := tryResolveAppURL(cmd)
	if appURL == "" {
		// 拿不到 app URL（如纯 token 登录、此前没配置过）。开不了浏览器——
		// 提示用户手动去网页建工作区。
		fmt.Fprintln(os.Stderr, "\nNo workspaces found.")
		fmt.Fprintln(os.Stderr, "Create a workspace in the web dashboard, then run 'multica login' again.")
		return nil, nil
	}

	createWorkspaceURL := appURL + "/workspaces/new"

	fmt.Fprintln(os.Stderr, "\nNo workspaces found. Opening workspace creation in your browser...")
	if err := openBrowser(createWorkspaceURL); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser automatically.\n")
	}
	fmt.Fprintf(os.Stderr, "If the browser didn't open, visit:\n  %s\n", createWorkspaceURL)
	fmt.Fprintln(os.Stderr, "\nWaiting for workspace creation...")

	// 轮询直到出现工作区或超时（5 分钟）。
	const pollInterval = 2 * time.Second
	const pollTimeout = 5 * time.Minute
	deadline := time.Now().Add(pollTimeout)

	// 每次轮询的请求预算：留 10s 下限保证循环保持灵敏（挂起的请求不该拖住单轮
	// 太久），但通过 AtLeastAPITimeout 仍尊重 MULTICA_HTTP_TIMEOUT——为慢网络把
	// 超时调高的用户不会被压回 10s 以下。整体等待仍由 pollTimeout 封顶。
	pollRequestTimeout := cli.AtLeastAPITimeout(10 * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		ctx, cancel := context.WithTimeout(context.Background(), pollRequestTimeout)
		var workspaces []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		err := client.GetJSON(ctx, "/api/workspaces", &workspaces)
		cancel()

		if err != nil {
			continue // 瞬时错误，继续轮询
		}
		if len(workspaces) > 0 {
			return workspaces, nil
		}
	}

	return nil, fmt.Errorf("timed out waiting for workspace creation")
}
