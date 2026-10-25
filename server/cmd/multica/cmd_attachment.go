// 文件职责：实现 `multica attachment` 命令组——download（按 ID 下载附件到本地
// 文件）与 upload（把本地文件上传为当前聊天任务的回复附件，返回可粘贴的 markdown
// 片段）。upload 面向在聊天任务里运行的 agent（任务 ID 取自 daemon 注入的
// MULTICA_TASK_ID）。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/codediff"
)

// attachmentCmd 是 `multica attachment` 父命令，子命令：download / upload。
var attachmentCmd = &cobra.Command{
	Use:   "attachment",
	Short: "Work with attachments",
}

var attachmentDownloadCmd = &cobra.Command{
	Use:   "download <attachment-id>",
	Short: "Download an attachment to a local file",
	Long:  "Download an attachment by its ID to a local file.",
	Example: `  # Download an image attachment to the current directory
  $ multica attachment download abc123

  # Download to a directory inside the working directory (keep agent
  # downloads out of /tmp and other machine-shared paths, MUL-4252)
  $ multica attachment download abc123 -o ./attachments`,
	Args: exactArgs(1),
	RunE: runAttachmentDownload,
}

var attachmentUploadCmd = &cobra.Command{
	Use:   "upload <path>",
	Short: "Upload a file to attach to your chat reply",
	Long: `Upload a local file so it is attached to the reply of the current chat task.

Intended for agents running inside a chat task: the file is tagged with the
task and, when the task completes, the server binds it to the assistant reply
it produces — it appears as an attachment card below your reply even if you
paste nothing. The command also returns a markdown snippet you may paste on its
own line to place the item: files use !file[name](url) (a card), images use
![name](url) (inline).

The task id is read from MULTICA_TASK_ID (set by the daemon inside a task);
override it with --task when needed.`,
	Example: `  # Attach an image to the current chat reply
  $ multica attachment upload ./chart.png`,
	Args: exactArgs(1),
	RunE: runAttachmentUpload,
}

func init() {
	attachmentCmd.AddCommand(attachmentDownloadCmd)
	attachmentCmd.AddCommand(attachmentUploadCmd)

	attachmentDownloadCmd.Flags().StringP("output-dir", "o", ".", "Directory to save the downloaded file")
	attachmentUploadCmd.Flags().String("task", "", "Chat task id to attach to (defaults to MULTICA_TASK_ID)")
}

// runAttachmentUpload 执行 `attachment upload <path>`：把本地文件上传并标记到
// 当前聊天任务上——任务完成时 server 会把附件绑到 assistant 回复上。返回 JSON
// （含可粘贴的 markdown 片段）：文件类用 !file[name](url) 卡片，图片用 ![name](url)
// 内联。
func runAttachmentUpload(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	taskID, _ := cmd.Flags().GetString("task")
	if taskID == "" {
		taskID = client.TaskID
	}
	if taskID == "" {
		return fmt.Errorf("no chat task in context: run inside a chat task (MULTICA_TASK_ID set) or pass --task <id>")
	}

	path := args[0]
	if isHTTPURL(path) {
		return fmt.Errorf("upload accepts a local file path, not a URL: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file %s: %w", path, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(60*time.Second))
	defer cancel()

	att, err := client.UploadChatAttachment(ctx, data, path, taskID)
	if err != nil {
		return fmt.Errorf("upload attachment: %w", err)
	}

	filename := filepath.Base(path)
	// 转义文件名里的 markdown 标签元字符：像 `report[v2].pdf` 这样的名字不会把
	// 片段标签截断。文件渲染成块级附件卡片 !file[...]( )；图片用 ![...]( ) 内联。
	label := escapeMarkdownLabel(filename)
	markdown := fmt.Sprintf("!file[%s](%s)", label, att.MarkdownURL)
	if strings.HasPrefix(att.ContentType, "image/") {
		markdown = fmt.Sprintf("![%s](%s)", label, att.MarkdownURL)
	}
	fmt.Fprintln(os.Stderr, "Uploaded:", filename)

	return cli.PrintJSON(os.Stdout, map[string]any{
		"id":           att.ID,
		"filename":     filename,
		"markdown_url": att.MarkdownURL,
		"markdown":     markdown,
	})
}

// escapeMarkdownLabel 转义 markdown 链接/图片标签中不允许裸用的元字符
// （[ ] ( ) 和反斜杠），使 `report[v2].pdf` 这样的文件名仍是一整个合法标签而不是
// 把片段截断。与渲染端的反转义集合保持一致
// （packages/ui/markdown/file-cards.ts）。
func escapeMarkdownLabel(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`[`, `\[`,
		`]`, `\]`,
		`(`, `\(`,
		`)`, `\)`,
	).Replace(s)
}

// runAttachmentDownload 执行 `attachment download <attachment-id>`：先取附件元数据
// （含签名的 download_url），再下载内容写到 -o 目录（目录不存在会自动创建）。
func runAttachmentDownload(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(60*time.Second))
	defer cancel()

	// 先取附件元数据（含签名 download_url）。
	var att map[string]any
	if err := client.GetJSON(ctx, "/api/attachments/"+args[0], &att); err != nil {
		return fmt.Errorf("get attachment: %w", err)
	}

	downloadURL := strVal(att, "download_url")
	if downloadURL == "" {
		return fmt.Errorf("attachment has no download URL")
	}

	filename := filepath.Base(strVal(att, "filename"))
	if filename == "" || filename == "." {
		filename = args[0]
	}

	// 下载文件内容。
	data, err := client.DownloadFile(ctx, downloadURL)
	if err != nil {
		return fmt.Errorf("download file: %w", err)
	}

	// 写到输出目录；目录不存在就先创建，让 `-o` 对着尚不存在的目录也能工作
	// （帮助示例里干净工作目录下的 `-o ./attachments` 就是这种情况）。
	outputDir, _ := cmd.Flags().GetString("output-dir")
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	destPath := filepath.Join(outputDir, filename)

	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	if attachmentIsRunInput(att, os.Getenv("MULTICA_INPUT_ISSUE_ID"), os.Getenv("MULTICA_TASK_ID"), os.Getenv("MULTICA_INPUT_STARTED_AT")) {
		if err := codediff.RecordInput(os.Getenv("MULTICA_INPUT_BASELINES"), destPath, data); err != nil {
			return fmt.Errorf("record attachment input: %w", err)
		}
	}
	// 打印绝对路径，agent 好引用该文件。
	abs, err := filepath.Abs(destPath)
	if err != nil {
		abs = destPath
	}
	fmt.Fprintln(os.Stderr, "Downloaded:", abs)

	// 再以 JSON 输出，兼容 --output json 的消费方。
	return cli.PrintJSON(os.Stdout, map[string]any{
		"id":       strVal(att, "id"),
		"filename": filename,
		"path":     abs,
		"size":     strVal(att, "size_bytes"),
	})
}

// 输入归属与时刻来自附件元数据，其他任务附件及本轮产物仍作为普通文件变化。
func attachmentIsRunInput(att map[string]any, issue, task, started string) bool {
	if issue == "" || strVal(att, "issue_id") != issue || strVal(att, "task_id") == task {
		return false
	}
	created, err := time.Parse(time.RFC3339Nano, strVal(att, "created_at"))
	if err != nil {
		return false
	}
	start, err := time.Parse(time.RFC3339Nano, started)
	return err == nil && !created.After(start)
}
