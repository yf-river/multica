package openviking

import (
	"context"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"path"
	"reflect"
	"sort"
	"strings"
)

type Skill struct {
	URI, Name, Content, Hash string
	OriginalURI              string
	Files                    []SkillFile
}
type SkillFile struct {
	Path string
	Data []byte
	Mode int32
}

func (c *Client) Skills(ctx context.Context, user string, selectName func(string) bool) ([]Skill, error) {
	var skills []Skill
	for _, root := range []string{"viking://agent/skills", "viking://user/" + user + "/skills"} {
		entries, err := c.List(ctx, user, root)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir || (selectName != nil && !selectName(entry.Name)) {
				continue
			}
			uri := entry.URI
			if uri == "" {
				uri = root + "/" + entry.Name
			}
			files := map[string][]byte{}
			queue := []string{uri}
			visited := map[string]bool{}
			for len(queue) > 0 {
				current := queue[0]
				queue = queue[1:]
				if visited[current] {
					continue
				}
				visited[current] = true
				children, err := c.List(ctx, user, current)
				if err != nil {
					return nil, err
				}
				for _, child := range children {
					childURI := child.URI
					if childURI == "" {
						childURI = current + "/" + child.Name
					}
					if !strings.HasPrefix(childURI, uri+"/") {
						return nil, fmt.Errorf("Skill listing escaped source root")
					}
					if child.IsDir {
						queue = append(queue, childURI)
						continue
					}
					rel := strings.TrimPrefix(childURI, uri+"/")
					if generatedSkillFile(rel) {
						continue
					}
					data, err := c.Download(ctx, user, childURI)
					if err != nil {
						return nil, err
					}
					files[rel] = data
				}
			}
			skill, err := readSkillFiles(uri, files)
			if err != nil {
				return nil, err
			}
			if skill.OriginalURI != "" {
				original, err := c.Download(ctx, user, skill.OriginalURI+"/SKILL.md")
				if err != nil {
					return nil, err
				}
				if EquivalentSkillMarkdown(original, []byte(skill.Content), path.Base(skill.URI)) {
					skill.Content = string(original)
				}
				skill.Hash = skillHash(skill)
			}
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

func readSkillFiles(uri string, files map[string][]byte) (Skill, error) {
	skill := Skill{URI: uri, Name: path.Base(uri)}
	modes := map[string]int32{}
	if raw, ok := files["multica-skill.json"]; ok {
		var metadata struct {
			Modes         map[string]int32 `json:"modes"`
			SourceSkillID string           `json:"source_skill_id"`
			SourceName    string           `json:"source_name"`
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return Skill{}, err
		}
		modes = metadata.Modes
		if metadata.SourceSkillID != "" {
			if !userPattern.MatchString(metadata.SourceSkillID) {
				return Skill{}, fmt.Errorf("invalid migration source Skill ID")
			}
			skill.OriginalURI = "viking://resources/migration/skill-" + SHA([]byte("multica://skill/" + metadata.SourceSkillID + uri))[:32]
		}
		if metadata.SourceName != "" {
			if !userPattern.MatchString(metadata.SourceName) {
				return Skill{}, fmt.Errorf("invalid source Skill name")
			}
			skill.Name = metadata.SourceName
		}
	}
	content, ok := files["SKILL.md"]
	if !ok {
		return Skill{}, fmt.Errorf("OpenViking Skill %s has no SKILL.md", uri)
	}
	skill.Content = string(content)
	for name, data := range files {
		if name == "SKILL.md" || name == "multica-skill.json" {
			continue
		}
		mode := modes[name]
		if mode == 0 {
			mode = 0644
			if strings.HasPrefix(string(data), "#!") {
				mode = 0755
			}
		}
		skill.Files = append(skill.Files, SkillFile{name, data, mode})
	}
	sort.Slice(skill.Files, func(i, j int) bool { return skill.Files[i].Path < skill.Files[j].Path })
	skill.Hash = skillHash(skill)
	return skill, nil
}

func skillHash(skill Skill) string {
	fingerprint := []byte(skill.Name + "\x00" + skill.Content)
	for _, f := range skill.Files {
		fingerprint = append(fingerprint, []byte(fmt.Sprintf("\x00%s\x00%d\x00%s", f.Path, f.Mode, SHA(f.Data)))...)
	}
	return SHA(fingerprint)
}

func generatedSkillFile(name string) bool {
	return path.Base(name) == ".abstract.md" || path.Base(name) == ".overview.md" || name == ".source.json" || name == ".meta.json"
}

// 原生 Skill 序列化允许规范化 YAML、边界空白及合法名称；正文与其它元数据必须一致。
func EquivalentSkillMarkdown(source, native []byte, name string) bool {
	a := strings.SplitN(string(source), "---", 3)
	b := strings.SplitN(string(native), "---", 3)
	if len(a) != 3 || len(b) != 3 || a[0] != "" || b[0] != "" {
		return false
	}
	var x, y map[string]any
	if yaml.Unmarshal([]byte(a[1]), &x) != nil || yaml.Unmarshal([]byte(b[1]), &y) != nil {
		return false
	}
	x["name"] = name
	for key, value := range y {
		if key == "allowed-tools" {
			a, aok := x[key].(string)
			b, bok := value.(string)
			if aok && bok && reflect.DeepEqual(strings.Fields(strings.ReplaceAll(a, ",", " ")), strings.Fields(strings.ReplaceAll(b, ",", " "))) {
				continue
			}
		}
		if !reflect.DeepEqual(x[key], value) {
			return false
		}
	}
	return strings.TrimSpace(a[2]) == strings.TrimSpace(b[2])
}
