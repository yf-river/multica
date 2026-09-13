// 技能可见性筛选：决定哪些技能会被渲染进 runtime brief、以哪个名称呈现，
// 并推导每个技能在磁盘上的目录 slug。渲染器保持"纯函数"，保证同一输入的
// brief 字节级一致（以维持提示缓存命中）。

package execenv

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// modelVisibleSkills 返回"模型可以调用的技能"，并把每个技能的 Name 归一化
// 成磁盘目录的 slug。
//
// 这个归一化不是表面功夫。runtime brief 会把这些条目渲染成"模型阅读并据此
// 挑选技能"的清单，而模型真正能调用的唯一名字，是 writeSkillFiles 铺下的
// 那个目录 slug——也就是同名字段的 sanitizeSkillName 结果。工作区技能的
// Name 是其人类可读的展示名（如 "PR review"），照原样列出等于递给模型一个
// 解析不到的标识符（MUL-5529）。
//
// slug 取自 resolveSkillSlugs 对"未过滤"整批技能的结果，因为 writeSkillFiles
// 会铺下每一个技能——包括在这里被隐藏的——而每个都会消耗一个 slug。若先
// 过滤再算，后缀就会错位，清单将与实际目录对不上。
//
// 已知缺口：resolveSkillSlugs 看不到文件系统，因此与"用户已安装"目录撞名的
// 技能仍会被写到 `<slug>-multica`，而这里的清单显示的是裸 slug。要弥合需要
// 把最终分配的 slug 从 Prepare 穿回来，而这些渲染器刻意够不到 Prepare——
// 它们保持纯函数，是为了 brief 跨运行字节一致。跟踪于 MUL-5550。
func modelVisibleSkills(skills []SkillContextForEnv) []SkillContextForEnv {
	if len(skills) == 0 {
		return nil
	}
	slugs := resolveSkillSlugs(skills)
	visible := make([]SkillContextForEnv, 0, len(skills))
	for i, skill := range skills {
		if skillModelInvocationVisible(skill) {
			skill.Name = slugs[i]
			visible = append(visible, skill)
		}
	}
	return visible
}

// resolveSkillSlugs 为一整批技能逐个分配"磁盘目录 slug"，并在批内去重，
// 使两个技能永远不宣称同一个目录。
//
// 单独用 sanitizeSkillName 不是单射的："A B" 与 "A-B" 都会被归约成 "a-b"。
// writeSkillFiles 在写入时通过 allocateCollisionFreeSkillDir 解决该冲突，
// 于是第二个技能落到 `a-b-multica`——但只用 sanitizeSkillName 搭的清单会
// 把两者都命名为 `a-b`，使第二个技能没有可调用的名字，还静默地把模型引到
// 第一个技能上。两侧都从这个函数推导，就能保持步调一致。该函数在批内是
// 确定的（只取决于索引顺序），因此相同输入下 brief 跨运行字节一致。
func resolveSkillSlugs(skills []SkillContextForEnv) []string {
	slugs := make([]string, len(skills))
	taken := make(map[string]struct{}, len(skills))
	for i, skill := range skills {
		base := sanitizeSkillName(skill.Name)
		slug := base
		for attempt := 1; ; attempt++ {
			if _, clash := taken[slug]; !clash {
				break
			}
			slug = skillSlugCandidate(base, attempt)
		}
		taken[slug] = struct{}{}
		slugs[i] = slug
	}
	return slugs
}

// skillModelInvocationVisible 判定技能是否允许被模型主动调用（读 frontmatter）。
func skillModelInvocationVisible(skill SkillContextForEnv) bool {
	return !skillDisablesModelInvocation(skill.Content)
}

// skillDisablesModelInvocation 解析技能正文的 frontmatter，看是否声明了
// disable-model-invocation: true，从而把技能从模型可见清单里剔除。
func skillDisablesModelInvocation(content string) bool {
	fmBody, _, ok := frontmatterParts(content)
	if !ok || strings.TrimSpace(fmBody) == "" {
		return false
	}
	var data map[string]any
	if err := yaml.Unmarshal([]byte(fmBody), &data); err != nil {
		return false
	}
	value, ok := data["disable-model-invocation"]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		// 兼容以字符串形式写的 "true"/"True" 等。
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}
