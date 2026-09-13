// Package skill 提供处理 SKILL.md 文件的共享工具（frontmatter 解析、二进制文件
// 判断等）。
package skill

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// 捕获组 1 里保留末尾换行很重要：yaml.v3 的 `|` clip chomping 只有在输入本身带
// 末尾换行时才保留它。
var frontmatterPattern = regexp.MustCompile(`(?s)\A---\r?\n(.*?\r?\n)---`)

// ParseSkillFrontmatter 从 SKILL.md 的 YAML frontmatter 块提取 name 与 description。
// frontmatter 缺失或畸形时返回空字符串，让调用方可以继续把缺失元数据当作非致命
// 条件，行为与旧版逐行解析器一致。
//
// 值先解码成通用 map 再按键逐项强制转换（标量用字面形式、序列/映射走 JSON），
// 而不是反序列化进字符串结构体。这样某个字段的结构化值不会丢掉合法的兄弟键，
// 且转换行为与 packages/core/skills/frontmatter.ts 里的 TS parseFrontmatter 对齐，
// 两端对同一输入判定一致。
func ParseSkillFrontmatter(content string) (name, description string) {
	if !strings.HasPrefix(content, "---") {
		return "", ""
	}
	match := frontmatterPattern.FindStringSubmatch(content)
	if match == nil {
		return "", ""
	}

	var fm map[string]any
	if err := yaml.Unmarshal([]byte(match[1]), &fm); err != nil {
		return "", ""
	}
	// 两个字段在消费处都是单行标签，而 YAML 块标量（`description: |`、
	// `description: >`）会因 clip chomping 带上末尾换行，需要 Trim。存下那个换行
	// 会让导入的 skill 与自身的精简形式不一致，被 skill 详情页当成未保存的编辑
	//（MUL-5645）。在解析接缝处归一化，任何导入路径都不用再记得这回事。
	return strings.TrimSpace(coerceFrontmatterValue(fm["name"])),
		strings.TrimSpace(coerceFrontmatterValue(fm["description"]))
}

// coerceFrontmatterValue 把解码后的 YAML 值渲染成字符串，与 TS 侧对齐：nil 变空串、
// 字符串原样、其它标量用字面形式、结构化值（序列/映射）走 JSON 编码。
func coerceFrontmatterValue(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case bool:
		return strconv.FormatBool(val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case uint64:
		return strconv.FormatUint(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'g', -1, 64)
	default:
		encoded, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
