// 文件职责：实现 `multica update` 命令——把本机 CLI 更新到最新版。可替换的
// updateDownloadTimeout（测试/调用方用）先于命令 flag 出现，默认沿用
// cli.DefaultUpdateDownloadTimeout。
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// updateDownloadTimeout 是发布包下载等待上限；init 里通过 --download-timeout flag
// 绑定（包级 var 便于测试替换）。
var updateDownloadTimeout time.Duration = cli.DefaultUpdateDownloadTimeout

// updateCmd 是 `multica update` 命令。
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update multica to the latest version",
	RunE:  runUpdate,
}

func init() {
	updateCmd.Flags().DurationVar(&updateDownloadTimeout, "download-timeout", cli.DefaultUpdateDownloadTimeout, "Maximum time to wait for the release archive download")
}

// runUpdate 执行 `update`：先去 GitHub 查最新版；Homebrew 安装走 brew upgrade，
// 否则从 GitHub Releases 下载二进制覆盖本机。
func runUpdate(_ *cobra.Command, _ []string) error {
	if updateDownloadTimeout <= 0 {
		return fmt.Errorf("download timeout must be greater than zero")
	}

	fmt.Fprintf(os.Stderr, "Current version: %s (commit: %s, built: %s)\n", version, commit, date)

	// 查最新版本（GitHub）。
	latest, err := cli.FetchLatestRelease()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not check latest version: %v\n", err)
	} else {
		latestVer := strings.TrimPrefix(latest.TagName, "v")
		currentVer := strings.TrimPrefix(version, "v")
		if currentVer == latestVer {
			fmt.Fprintln(os.Stderr, "Already up to date.")
			return nil
		}
		fmt.Fprintf(os.Stderr, "Latest version:  %s\n\n", latest.TagName)
	}

	// 探测安装方式并据此更新。
	if cli.IsBrewInstall() {
		fmt.Fprintln(os.Stderr, "Updating via Homebrew...")
		output, err := cli.UpdateViaBrew()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", output)
			return fmt.Errorf("brew upgrade failed: %w\nYou can try manually: brew upgrade multica-ai/tap/multica", err)
		}
		fmt.Fprintln(os.Stderr, "Update complete.")
		return nil
	}

	// 非 brew 安装——直接从 GitHub Releases 下载二进制。
	if latest == nil {
		return fmt.Errorf("could not determine latest version; check https://github.com/multica-ai/multica/releases/latest")
	}
	targetVersion := latest.TagName
	fmt.Fprintf(os.Stderr, "Downloading %s from GitHub Releases...\n", targetVersion)
	output, err := cli.UpdateViaDownloadWithTimeout(targetVersion, updateDownloadTimeout)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s\nUpdate complete.\n", output)
	return nil
}
