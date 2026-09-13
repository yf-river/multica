// 文件职责：实现 `multica issue metadata` 子命令组——每个 issue 上挂一张 KV
// 元数据表（供 agent 流水线跨运行保存状态），本文件提供 list/get/set/delete 四个
// 子命令、把 CLI 的 --value/--type 转成 JSON 类型的 parseMetadataValue，以及 KV 表
// 的表格渲染辅助。约束（key 正则、50 个上限、仅原始类型值、8KB 上限）见
// server/internal/handler/issue_metadata.go。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// buildMetadataFilterQueryParam 把可重复的 `--metadata key=value` flag 汇总成一个
// canonical JSON 对象，作为 /api/issues 的 `metadata` 查询参数（供 `issue list`
// 按元数据过滤）。值的类型判定与 `metadata set --value`（不带 --type）一致——能按
// JSON 解析（true/数字/字符串）就按 JSON，否则按字符串。重复 key 直接报错：
// 过滤是 AND 语义，同一 key 出现多次说明用户想表达的条件无法用并列表达。
func buildMetadataFilterQueryParam(pairs []string) (string, error) {
	if len(pairs) == 0 {
		return "", nil
	}
	out := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		idx := strings.IndexByte(pair, '=')
		if idx <= 0 {
			return "", fmt.Errorf("--metadata %q must be in key=value form", pair)
		}
		key := pair[:idx]
		raw := pair[idx+1:]
		if _, dup := out[key]; dup {
			return "", fmt.Errorf("--metadata key %q given more than once; combine into a single filter", key)
		}
		encoded, err := parseMetadataValue(raw, "")
		if err != nil {
			return "", fmt.Errorf("--metadata %s: %w", key, err)
		}
		var v any
		if err := json.Unmarshal(encoded, &v); err != nil {
			return "", fmt.Errorf("--metadata %s: encode value: %w", key, err)
		}
		out[key] = v
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode metadata filter: %w", err)
	}
	return string(buf), nil
}

// issueMetadataCmd 是 `issue metadata` 分组命令——管理挂在 issue 上的 KV 元数据表，
// 子命令：list / get / set / delete。约束（key 正则、50 key 上限、仅支持原始类型值、
// 8KB 上限）见 server/internal/handler/issue_metadata.go。
var issueMetadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Manage per-issue metadata (KV)",
}

var issueMetadataListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List all metadata keys on an issue",
	Args:  exactArgs(1),
	RunE:  runIssueMetadataList,
}

var issueMetadataGetCmd = &cobra.Command{
	Use:   "get <issue-id>",
	Short: "Get a single metadata key value",
	Args:  exactArgs(1),
	RunE:  runIssueMetadataGet,
}

var issueMetadataSetCmd = &cobra.Command{
	Use:   "set <issue-id>",
	Short: "Set a single metadata key value",
	Long: `Set a single metadata key value. The value is JSON-parsed by default:
  --value true / --value false  → bool
  --value 3 / --value 3.14      → number
  --value waiting               → string
Use --type to force a specific type. Quote like '"42"' (JSON-escaped) to force
a string when the bare value would otherwise sniff as a number or bool.`,
	Args: exactArgs(1),
	RunE: runIssueMetadataSet,
}

var issueMetadataDeleteCmd = &cobra.Command{
	Use:   "delete <issue-id>",
	Short: "Delete a single metadata key",
	Args:  exactArgs(1),
	RunE:  runIssueMetadataDelete,
}

func init() {
	issueMetadataCmd.AddCommand(issueMetadataListCmd)
	issueMetadataCmd.AddCommand(issueMetadataGetCmd)
	issueMetadataCmd.AddCommand(issueMetadataSetCmd)
	issueMetadataCmd.AddCommand(issueMetadataDeleteCmd)

	issueMetadataListCmd.Flags().String("output", "table", "Output format: table or json")
	issueMetadataGetCmd.Flags().String("output", "json", "Output format: table or json")
	issueMetadataGetCmd.Flags().String("key", "", "Metadata key (required)")
	issueMetadataSetCmd.Flags().String("output", "table", "Output format: table or json")
	issueMetadataSetCmd.Flags().String("key", "", "Metadata key (required)")
	issueMetadataSetCmd.Flags().String("value", "", "Metadata value (required)")
	issueMetadataSetCmd.Flags().String("type", "", "Force value type: string, number, or bool (default: auto-infer via JSON parsing)")
	issueMetadataDeleteCmd.Flags().String("output", "table", "Output format: table or json")
	issueMetadataDeleteCmd.Flags().String("key", "", "Metadata key (required)")

	issueCmd.AddCommand(issueMetadataCmd)
}

// parseMetadataValue 把 CLI 的 --value（以及可选的 --type）转成发给 server 的
// JSON 编码载荷。默认「嗅探」类型：裸文本能按 JSON bool/数字/字符串解析就用对应
// 类型；其余 JSON 形态（null、数组、对象）即使走默认路径也被拒——server 本就会拒。
//
// forcedType 非空时覆盖嗅探："string" 原样包成 JSON 字符串；"number" 严格要求能
// 解析为数字；"bool" 只接受字面 "true" / "false"。
func parseMetadataValue(raw, forcedType string) (json.RawMessage, error) {
	switch forcedType {
	case "string":
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("encode string value: %w", err)
		}
		return buf, nil
	case "number":
		if _, err := strconv.ParseFloat(raw, 64); err != nil {
			return nil, fmt.Errorf("value %q is not a valid number", raw)
		}
		return json.RawMessage(raw), nil
	case "bool":
		if raw != "true" && raw != "false" {
			return nil, fmt.Errorf("value %q is not a valid bool (expected true or false)", raw)
		}
		return json.RawMessage(raw), nil
	case "":
		// 无 --type：落到下面的自动推断。
	default:
		return nil, fmt.Errorf("unknown --type %q (expected string, number, or bool)", forcedType)
	}

	// 自动推断：先试 JSON 解析，能解析成字符串/bool/数字就直接用原样文本，
	// 否则整体当字符串（如 "waiting"）。
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		switch v.(type) {
		case string, bool, float64:
			return json.RawMessage(raw), nil
		}
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode string value: %w", err)
	}
	return buf, nil
}

// runIssueMetadataList 执行 `issue metadata list <issue-id>`：列出 issue 上的全部
// 元数据 KV。见函数体内对 404 的「尽力降级」处理——那是给 agent 引导路径的保底。
func runIssueMetadataList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/metadata", &result); err != nil {
		// 尽力降级：当 server 没暴露 per-issue metadata 端点（老版本自托管后端、
		// 缺迁移、或路由问题表现为 404）时，agent 引导阶段的「metadata list」绝不能
		// 让整次 run 失败。这里输出空 map 并以 0 退出，agent 仍能拿到「元数据为空」
		// 的信号（和新建 issue 一致）。其余状态码（鉴权失败、server 错误）保留真实
		// 错误语义；get/set/delete 不受影响——那些调用方仍需要知道何时出错了。
		var httpErr *cli.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			output, _ := cmd.Flags().GetString("output")
			empty := map[string]any{}
			if output == "json" {
				return cli.PrintJSON(os.Stdout, empty)
			}
			printMetadataTable(empty)
			return nil
		}
		return fmt.Errorf("list metadata: %w", err)
	}
	metadata, _ := result["metadata"].(map[string]any)
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, metadata)
	}
	printMetadataTable(metadata)
	return nil
}

// runIssueMetadataGet 执行 `issue metadata get <issue-id> --key <k>`：读单个元数据
// key 的值，key 不存在时报错。
func runIssueMetadataGet(cmd *cobra.Command, args []string) error {
	key, _ := cmd.Flags().GetString("key")
	if key == "" {
		return fmt.Errorf("--key is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	var result map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/metadata", &result); err != nil {
		return fmt.Errorf("get metadata: %w", err)
	}
	metadata, _ := result["metadata"].(map[string]any)
	value, present := metadata[key]
	if !present {
		return fmt.Errorf("key %q not found on issue", key)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, value)
	}
	headers := []string{"KEY", "VALUE", "TYPE"}
	rows := [][]string{{key, formatMetadataValue(value), metadataValueType(value)}}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// runIssueMetadataSet 执行 `issue metadata set <issue-id> --key <k> --value <v>`
// （可加 --type 强制类型）：写入单个元数据 key，成功后展示刷新后的整张表。
// --value 为空也算合法值，故用 Flags().Changed 而非空串判断是否提供。
func runIssueMetadataSet(cmd *cobra.Command, args []string) error {
	key, _ := cmd.Flags().GetString("key")
	if key == "" {
		return fmt.Errorf("--key is required")
	}
	if !cmd.Flags().Changed("value") {
		return fmt.Errorf("--value is required")
	}
	rawValue, _ := cmd.Flags().GetString("value")
	forcedType, _ := cmd.Flags().GetString("type")
	value, err := parseMetadataValue(rawValue, forcedType)
	if err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	body := map[string]any{"value": value}
	var result map[string]any
	path := "/api/issues/" + issueRef.ID + "/metadata/" + key
	if err := client.PutJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("set metadata: %w", err)
	}
	metadata, _ := result["metadata"].(map[string]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, metadata)
	}
	printMetadataTable(metadata)
	return nil
}

// runIssueMetadataDelete 执行 `issue metadata delete <issue-id> --key <k>`：删除
// 单个元数据 key。删除成功后刷新整张表展示；刷新失败仍打印清晰的删除成功消息。
func runIssueMetadataDelete(cmd *cobra.Command, args []string) error {
	key, _ := cmd.Flags().GetString("key")
	if key == "" {
		return fmt.Errorf("--key is required")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	path := "/api/issues/" + issueRef.ID + "/metadata/" + key
	if err := client.DeleteJSON(ctx, path); err != nil {
		return fmt.Errorf("delete metadata: %w", err)
	}

	// 刷新一下整张表，让用户直接看到删除结果。
	var result map[string]any
	output, _ := cmd.Flags().GetString("output")
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/metadata", &result); err != nil {
		if output == "json" {
			return cli.PrintJSON(os.Stdout, map[string]any{"deleted": true})
		}
		fmt.Fprintln(os.Stdout, "Key deleted.")
		return nil
	}
	metadata, _ := result["metadata"].(map[string]any)
	if output == "json" {
		return cli.PrintJSON(os.Stdout, metadata)
	}
	printMetadataTable(metadata)
	return nil
}

// printMetadataTable 把整张元数据 KV 表渲染成 KEY/VALUE/TYPE 三列，key 按字典序
// 排列保证输出稳定。
func printMetadataTable(metadata map[string]any) {
	headers := []string{"KEY", "VALUE", "TYPE"}
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		v := metadata[k]
		rows = append(rows, []string{k, formatMetadataValue(v), metadataValueType(v)})
	}
	cli.PrintTable(os.Stdout, headers, rows)
}

// formatMetadataValue 把元数据值渲染成可读文本；用于表格展示与 issue usage 的
// token 统计列（整数形式的计数不会出现 3.000000e+06 这类科学计数法）。
func formatMetadataValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		// JSON 数字解出来是 float64；整数去掉末尾 ".0"，显示更自然。
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		buf, _ := json.Marshal(v)
		return string(buf)
	}
}

// metadataValueType 返回元数据值的类型标签（表格 TYPE 列用），与 server 端约定的
// 原始类型命名保持一致：string / bool / number，其余归 unknown。
func metadataValueType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "number"
	default:
		return "unknown"
	}
}
