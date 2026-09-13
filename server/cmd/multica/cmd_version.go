// 文件职责：实现 `multica version` 命令——打印版本/commit/构建日期/Go 与平台信息，
// 支持 --output text|json。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

func init() {
	versionCmd.Flags().String("output", "text", "Output format: text or json")
}

// versionCmd 是 `multica version` 命令。
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	RunE:  runVersion,
}

// runVersion 执行 `version`：--output json 输出结构化信息（便于脚本判断），
// 默认打人类可读文本。
func runVersion(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")

	if output == "json" {
		info := map[string]string{
			"version": version,
			"commit":  commit,
			"date":    date,
			"go":      runtime.Version(),
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	fmt.Printf("multica %s (commit: %s, built: %s)\n", version, commit, date)
	fmt.Printf("go: %s, os/arch: %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return nil
}
