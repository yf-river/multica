// flags.go 提供命令行 flag 与同名环境变量、默认值之间的「取值优先级」小工具，
// 供 cli 各命令解析配置时统一使用。
package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// FlagOrEnv 返回 flag 的值；若该 flag 没在命令行显式给出，就回退读环境变量；
// 环境变量也没设或为空，就返回 fallback 默认值。
func FlagOrEnv(cmd *cobra.Command, flagName, envKey, fallback string) string {
	if cmd.Flags().Changed(flagName) {
		val, _ := cmd.Flags().GetString(flagName)
		return val
	}
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v
	}
	return fallback
}
