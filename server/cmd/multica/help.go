// 文件职责：CLI 帮助系统的定制——仿 gh 风格的分组帮助输出。定义命令分组常量、
// errSilent 哨兵错误、exactArgs 位置参数校验器，注册帮助模板使用的两个模板函数
// （formatCommandList / commandsInGroup），并提供根/子/叶子三种 help 模板字符串。
package main

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
)

// 全 CLI 共用的命令分组 ID（rootCmd 帮助输出按组排列）。
const (
	groupCore       = "core"
	groupRuntime    = "runtime"
	groupAdditional = "additional"
)

// errSilent 表示错误消息已经打印过（调用方不再重复打印）。
var errSilent = fmt.Errorf("")

// exactArgs 返回校验位置参数个数的 cobra.PositionalArgs：个数不符时打印错误行并
// 展示帮助，让用户看到用法上下文——然后返回 errSilent，避免错误消息被重复打两遍。
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			if n == 1 {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: accepts 1 arg, received %d\n\n", len(args))
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: accepts %d args, received %d\n\n", n, len(args))
			}
			cmd.Help()
			return errSilent
		}
		return nil
	}
}

// initHelp 把根命令配置成 gh 风格帮助：套 help/usage 模板、隐藏默认 completion
// 命令、注册三个分组，并递归下发到所有子命令。
func initHelp(root *cobra.Command) {
	root.SetHelpTemplate(rootHelpTemplate)
	root.SetUsageTemplate(rootHelpTemplate)
	root.CompletionOptions.HiddenDefaultCmd = true

	root.AddGroup(
		&cobra.Group{ID: groupCore, Title: "CORE COMMANDS"},
		&cobra.Group{ID: groupRuntime, Title: "RUNTIME COMMANDS"},
		&cobra.Group{ID: groupAdditional, Title: "ADDITIONAL COMMANDS"},
	)

	// 递归应用 gh 风格模板：有子命令的用 subHelpTemplate，叶子用 leafHelpTemplate。
	applyTemplates(root)
}

// applyTemplates 递归给命令树套模板：含子命令者用「分组/子命令」样式，叶子命令
// 用「单命令」样式。
func applyTemplates(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		if c.HasSubCommands() {
			c.SetHelpTemplate(subHelpTemplate)
			c.SetUsageTemplate(subHelpTemplate)
		} else {
			c.SetHelpTemplate(leafHelpTemplate)
			c.SetUsageTemplate(leafHelpTemplate)
		}
		applyTemplates(c)
	}
}

// formatCommandList 按 "name:  description" 样式格式化命令清单（自动对齐、跳过
// 不可用命令），对齐 gh 的输出。
func formatCommandList(cmds []*cobra.Command) string {
	if len(cmds) == 0 {
		return ""
	}

	maxLen := 0
	for _, c := range cmds {
		if c.IsAvailableCommand() && len(c.Name()) > maxLen {
			maxLen = len(c.Name())
		}
	}

	var b strings.Builder
	for _, c := range cmds {
		if !c.IsAvailableCommand() {
			continue
		}
		padding := strings.Repeat(" ", maxLen-len(c.Name()))
		fmt.Fprintf(&b, "  %s:%s  %s\n", c.Name(), padding, c.Short)
	}
	return b.String()
}

// commandsInGroup 从命令列表里筛出属于指定分组、且当前可用的命令。
func commandsInGroup(cmds []*cobra.Command, groupID string) []*cobra.Command {
	var result []*cobra.Command
	for _, c := range cmds {
		if c.GroupID == groupID && c.IsAvailableCommand() {
			result = append(result, c)
		}
	}
	return result
}

func init() {
	// 把两个模板函数注册进 cobra 的模板函数表，help 模板里即可直接引用。
	cobra.AddTemplateFuncs(template.FuncMap{
		"formatCommandList": formatCommandList,
		"commandsInGroup":   commandsInGroup,
	})
}

var rootHelpTemplate = `Work seamlessly with Multica from the command line.

USAGE
  multica <command> <subcommand> [flags]
{{range .Groups}}
{{.Title}}
{{formatCommandList (commandsInGroup $.Commands .ID)}}
{{- end}}
FLAGS
{{.LocalFlags.FlagUsages}}
EXAMPLES
  $ multica login
  $ multica issue list --output json
  $ multica daemon start
  $ multica agent list --output json

ENVIRONMENT VARIABLES
  MULTICA_SERVER_URL    Override the default server URL
  MULTICA_WORKSPACE_ID  Set the active workspace

LEARN MORE
  Use ` + "`multica <command> <subcommand> --help`" + ` for more information about a command.
`

var subHelpTemplate = `{{.Short}}

USAGE
  {{.CommandPath}} <command> [flags]

COMMANDS
{{formatCommandList .Commands}}
{{- if .HasLocalFlags}}

FLAGS
{{.LocalFlags.FlagUsages}}
{{- end}}
INHERITED FLAGS
  --help   Show help for command
{{- if .Example}}

EXAMPLES
{{.Example}}
{{- end}}

LEARN MORE
  Use ` + "`{{.CommandPath}} <command> --help`" + ` for more information about a command.
`

var leafHelpTemplate = `{{if .Long}}{{.Long}}{{else}}{{.Short}}{{end}}

USAGE
  {{.UseLine}}
{{- if .HasLocalFlags}}

FLAGS
{{.LocalFlags.FlagUsages}}
{{- end}}
INHERITED FLAGS
  --help   Show help for command
{{- if .Example}}

EXAMPLES
{{.Example}}
{{- end}}

LEARN MORE
  Use ` + "`multica <command> <subcommand> --help`" + ` for more information about a command.
`
