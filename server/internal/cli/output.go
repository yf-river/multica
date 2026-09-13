// output.go 提供 cli 命令的两种统一输出方式：对齐的文本表格（给终端人看）与缩进
// 美化过的 JSON（给脚本/管道消费），避免各子命令各写一套输出格式。
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// PrintTable 把「表头 + 若干行」以等宽对齐的表格形式写到 w（用 tabwriter 保证列对齐）。
func PrintTable(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()
}

// PrintJSON 把 v 以两空格缩进的 JSON 写到 w。
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
