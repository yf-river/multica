// 文件职责：CLI 程序入口与命令树组装。rootCmd 在这里收集全部顶层命令并做 help
// 分组；版本/commit/date 由构建注入。main() 除了执行命令树，还特殊处理 daemon
// 的「任务环境准备 helper」（见 execenv），并负责统一收尾：清过期的更新产物、
// 以合适格式打印错误与退出码。
package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// debugFlag 绑定到持久化 --debug flag；置位时 FormatError 输出完整原始错误链
// 而不是只有面向用户的一句话。
var debugFlag bool

// rootCmd 是所有命令的根；SilenceUsage/Errors 让它只在需要时自己打印帮助与错误。
var rootCmd = &cobra.Command{
	Use:           "multica",
	Short:         "Multica CLI — local agent runtime and management tool",
	Long:          "Work seamlessly with Multica from the command line.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)\ngo: %s, os/arch: %s/%s", version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	rootCmd.SetVersionTemplate("multica {{.Version}}\n")

	// 给 CLI 发出的每个 HTTP 请求打上本二进制构建版本，让 server 能按客户端
	// 版本拆分日志/指标。
	cli.ClientVersion = version

	rootCmd.PersistentFlags().String("server-url", "", "Multica server URL (env: MULTICA_SERVER_URL)")
	rootCmd.PersistentFlags().String("workspace-id", "", "Workspace ID (env: MULTICA_WORKSPACE_ID)")
	rootCmd.PersistentFlags().String("profile", "", "Configuration profile name (e.g. dev) — isolates config, daemon state, and workspaces")
	rootCmd.PersistentFlags().BoolVar(&debugFlag, "debug", false, "Print full error details on failure (env: MULTICA_DEBUG)")

	// 核心（Core）命令组
	issueCmd.GroupID = groupCore
	projectCmd.GroupID = groupCore
	labelCmd.GroupID = groupCore
	propertyCmd.GroupID = groupCore
	agentCmd.GroupID = groupCore
	autopilotCmd.GroupID = groupCore
	workspaceCmd.GroupID = groupCore
	repoCmd.GroupID = groupCore
	skillCmd.GroupID = groupCore
	squadCmd.GroupID = groupCore
	lifeCmd.GroupID = groupCore
	chatCmd.GroupID = groupCore

	// 运行时（Runtime）命令组
	daemonCmd.GroupID = groupRuntime
	runtimeCmd.GroupID = groupRuntime

	// 附加（Additional）命令组
	authCmd.GroupID = groupAdditional
	userCmd.GroupID = groupAdditional
	loginCmd.GroupID = groupAdditional
	setupCmd.GroupID = groupAdditional
	attachmentCmd.GroupID = groupAdditional
	configCmd.GroupID = groupAdditional
	updateCmd.GroupID = groupAdditional
	versionCmd.GroupID = groupAdditional

	rootCmd.AddCommand(issueCmd)
	rootCmd.AddCommand(projectCmd)
	rootCmd.AddCommand(labelCmd)
	rootCmd.AddCommand(propertyCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(autopilotCmd)
	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(repoCmd)
	rootCmd.AddCommand(skillCmd)
	rootCmd.AddCommand(squadCmd)
	rootCmd.AddCommand(lifeCmd)
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(runtimeCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(userCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(attachmentCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(versionCmd)

	initHelp(rootCmd)
}

// main 是程序入口：优先接管 daemon 的「任务环境准备 helper」调用（由内部参数
// 识别）；否则清理过期更新产物后执行命令树，错误统一经 cli.FormatError 打印、
// 退出码由 cli.ExitCodeFor 决定（errSilent 表示消息已打过，不再重复打印）。
func main() {
	if len(os.Args) == 2 && os.Args[1] == execenv.PreparationHelperArg {
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		if err := execenv.RunPreparationHelper(os.Stdin, os.Stdout, logger); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cli.CleanupStaleUpdateArtifacts()
	if err := rootCmd.Execute(); err != nil {
		if err != errSilent {
			fmt.Fprintln(os.Stderr, cli.FormatError(err, debugFlag))
		}
		os.Exit(cli.ExitCodeFor(err))
	}
}
