// openviking-migrate 按显式清单迁移；原归属不明确的数据不进入执行清单。
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/openviking"
)

type Plan struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}
type Entry struct {
	Kind      string            `json:"kind"`
	Source    string            `json:"source"`
	SourceURI string            `json:"source_uri"`
	User      string            `json:"user"`
	Target    string            `json:"target"`
	Hashes    map[string]string `json:"hashes"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	planPath := flag.String("plan", "", "显式来源、归属和目标清单")
	apply := flag.Bool("apply", false, "写入目标 OpenViking；默认仅校验清单和全部源哈希")
	flag.Parse()
	raw, err := os.ReadFile(*planPath)
	if err != nil {
		return err
	}
	var plan Plan
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return err
	}
	if plan.Version != 1 {
		return fmt.Errorf("unsupported plan version")
	}
	c := openviking.FromEnv()
	c.HTTP.Timeout = 10 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	targets := map[string]bool{}
	for _, entry := range plan.Entries {
		if _, err = openviking.User("migration", entry.User); err != nil {
			return err
		}
		if entry.Kind != "skill" && entry.Kind != "resource" && entry.Kind != "session" {
			return fmt.Errorf("unsupported migration kind %s", entry.Kind)
		}
		if targets[entry.Target] {
			return fmt.Errorf("duplicate migration target %s", entry.Target)
		}
		targets[entry.Target] = true
		files, err := sourceFiles(entry)
		if err != nil {
			return err
		}
		if !*apply {
			fmt.Printf("validated %s %s: %d original files\n", entry.Kind, entry.Target, len(files))
			continue
		}
		if err = c.Health(ctx); err != nil {
			return err
		}
		switch entry.Kind {
		case "skill":
			err = migrateSkill(ctx, c, entry, files)
		case "resource":
			if !strings.HasPrefix(entry.Target, "viking://resources/") {
				return fmt.Errorf("resource target must be shared resources")
			}
			err = importOriginals(ctx, c, entry.User, entry.Target, files)
			if err == nil {
				err = c.JSON(ctx, entry.User, "POST", "/api/v1/content/reindex", map[string]any{"uri": entry.Target, "mode": "semantic_and_vectors", "recursive": true, "wait": true}, nil)
			}
		case "session":
			err = migrateSession(ctx, c, entry, files)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", entry.Target, err)
		}
		fmt.Printf("verified %s %s: %d original files\n", entry.Kind, entry.Target, len(files))
	}
	return nil
}
func sourceFiles(entry Entry) (map[string]string, error) {
	if !filepath.IsAbs(entry.Source) || len(entry.Hashes) == 0 {
		return nil, fmt.Errorf("absolute source and complete hashes required")
	}
	files := map[string]string{}
	err := filepath.WalkDir(entry.Source, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source is not a regular file: %s", p)
		}
		rel, err := filepath.Rel(entry.Source, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if entry.Hashes[rel] != openviking.SHA(data) {
			return fmt.Errorf("missing or mismatched source hash: %s", rel)
		}
		files[rel] = p
		return nil
	})
	if err == nil && len(files) != len(entry.Hashes) {
		err = fmt.Errorf("source inventory count changed")
	}
	return files, err
}
func importOriginals(ctx context.Context, c *openviking.Client, user, target string, files map[string]string) error {
	return c.StoreOriginals(ctx, user, target, files)
}
func migrateSkill(ctx context.Context, c *openviking.Client, e Entry, files map[string]string) error {
	if !strings.HasPrefix(e.Target, "viking://agent/skills/") && !strings.HasPrefix(e.Target, "viking://user/"+e.User+"/skills/") {
		return fmt.Errorf("invalid skill target")
	}
	// 原始包先留存，原生解析器拒绝内容时也有可核对的来源。
	original := "viking://resources/migration/skill-" + openviking.SHA([]byte(e.SourceURI + e.Target))[:32]
	if err := c.StoreOriginals(ctx, e.User, original, files); err != nil {
		return err
	}
	if _, err := c.Download(ctx, e.User, e.Target+"/SKILL.md"); err == nil {
		// 仅本清单拥有的导入可继续恢复；其它已存在 Skill 不覆盖。
		local, ok := files["multica-skill.json"]
		if !ok {
			return fmt.Errorf("existing skill has no migration ownership marker")
		}
		want, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		got, err := c.Download(ctx, e.User, e.Target+"/multica-skill.json")
		if err != nil || !bytes.Equal(want, got) {
			return fmt.Errorf("existing skill ownership differs")
		}
		return verifySkill(ctx, c, e, files)
	} else if h, ok := err.(*openviking.HTTPError); !ok || h.Code != 404 {
		return err
	}

	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for rel, local := range files {
		data, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		if rel == "SKILL.md" {
			data = regexp.MustCompile(`(?m)^name:[^\n]*$`).ReplaceAll(data, []byte("name: "+path.Base(e.Target)))
		}
		header := &zip.FileHeader{Name: path.Base(e.Target) + "/" + rel, Method: zip.Deflate}
		info, err := os.Stat(local)
		if err != nil {
			return err
		}
		header.SetMode(info.Mode())
		w, err := z.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err = w.Write(data); err != nil {
			return err
		}
	}
	if err := z.Close(); err != nil {
		return err
	}
	id, err := c.Upload(ctx, e.User, "skill.zip", &buffer)
	if err != nil {
		return err
	}
	if err = c.JSON(ctx, e.User, "POST", "/api/v1/skills", map[string]any{"temp_file_id": id, "target_uri": e.Target, "wait": false}, nil); err != nil {
		return err
	}
	return verifySkill(ctx, c, e, files)
}

func verifySkill(ctx context.Context, c *openviking.Client, e Entry, files map[string]string) error {
	for rel, local := range files {
		want, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		got, err := c.Download(ctx, e.User, e.Target+"/"+rel)
		if err == nil && rel == "SKILL.md" && !bytes.Equal(want, got) {
			if openviking.EquivalentSkillMarkdown(want, got, path.Base(e.Target)) {
				continue
			}
		}

		if err != nil || !bytes.Equal(want, got) {
			return fmt.Errorf("native skill changed source file %s; original package preserved: %v", rel, err)
		}
	}
	return nil
}
func migrateSession(ctx context.Context, c *openviking.Client, e Entry, files map[string]string) error {
	prefix := "viking://user/" + e.User + "/sessions/"
	if !strings.HasPrefix(e.Target, prefix) || strings.Contains(strings.TrimPrefix(e.Target, prefix), "/") || e.SourceURI == "" {
		return fmt.Errorf("explicit owned session and source URI required")
	}
	session := strings.TrimPrefix(e.Target, prefix)
	if err := c.JSON(ctx, e.User, "POST", "/api/v1/sessions", map[string]any{"session_id": session, "auto_commit_policy": nil}, nil); err != nil {
		if h, ok := err.(*openviking.HTTPError); !ok || h.Code != 409 {
			return err
		}
	}
	revision, _ := json.Marshal(map[string]any{"source_uri": e.SourceURI, "hashes": e.Hashes})
	root := e.Target + "/migration/source-" + openviking.SHA(revision)[:24]
	if err := storeSessionOriginals(ctx, c, e, root, files); err != nil {
		return err
	}
	names := []string{}
	for rel := range files {
		if rel == "messages.jsonl" || strings.HasPrefix(rel, "history/") && strings.HasSuffix(rel, "/messages.jsonl") {
			names = append(names, rel)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "messages.jsonl" {
			return false
		}
		if names[j] == "messages.jsonl" {
			return true
		}
		return names[i] < names[j]
	})
	// 回读目标已有来源，实现中断后的幂等续传。
	var seen = map[string]bool{}
	roots := []string{e.Target}
	archives, err := c.List(ctx, e.User, e.Target+"/history")
	if err != nil {
		return err
	}
	for _, a := range archives {
		if a.IsDir {
			roots = append(roots, a.URI)
		}
	}
	for _, r := range roots {
		data, err := c.Download(ctx, e.User, r+"/messages.jsonl")
		if h, ok := err.(*openviking.HTTPError); ok && h.Code == 404 {
			continue
		}
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			var m openviking.Message
			if json.Unmarshal(line, &m) == nil {
				for _, id := range m.Sources {
					seen[id] = true
				}
			}
		}
	}
	for _, name := range names {
		data, err := os.ReadFile(files[name])
		if err != nil {
			return err
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var m openviking.Message
			rewritten := bytes.ReplaceAll(line, []byte(e.SourceURI), []byte(root))
			if err = json.Unmarshal(rewritten, &m); err != nil {
				return err
			}
			id := "migration:" + openviking.SHA(append([]byte(e.SourceURI+"/"+name+"\x00"), line...))
			if seen[id] {
				continue
			}
			m.Sources = append(m.Sources, id)
			if err = c.JSON(ctx, e.User, "POST", "/api/v1/sessions/"+url.PathEscape(session)+"/messages/batch", map[string]any{"messages": []openviking.Message{m}}, nil); err != nil {
				return err
			}
			seen[id] = true
		}
	}
	// 历史会话不自动二次提炼；先完成全部原件与会话对账，提炼由单独验收决定。
	return nil
}

// 原生生成的摘要文件不能直接写入；标准 ZIP 保留完整源目录及原始身份元数据。
func storeSessionOriginals(ctx context.Context, c *openviking.Client, e Entry, root string, files map[string]string) error {
	zipFile, err := os.CreateTemp("", "multica-session-originals-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(zipFile.Name())
	defer zipFile.Close()
	z := zip.NewWriter(zipFile)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	stored := map[string]string{}
	for _, rel := range names {
		data, err := os.ReadFile(files[rel])
		if err != nil {
			return err
		}
		info, err := os.Stat(files[rel])
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		header.SetMode(info.Mode())
		w, err := z.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err = w.Write(data); err != nil {
			return err
		}
		if path.Base(rel) != ".abstract.md" && path.Base(rel) != ".overview.md" {
			stored[rel] = files[rel]
		}
	}
	if err = z.Close(); err != nil {
		return err
	}
	if err = zipFile.Sync(); err != nil {
		return err
	}
	zipName := "multica-session-originals.zip"
	manifestName := "multica-session-originals.json"
	if files[zipName] != "" || files[manifestName] != "" {
		return fmt.Errorf("source contains reserved migration container name")
	}
	manifestFile, err := os.CreateTemp("", "multica-session-manifest-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(manifestFile.Name())
	defer manifestFile.Close()
	manifest, err := json.Marshal(map[string]any{"source_uri": e.SourceURI, "target": e.Target, "files": e.Hashes, "zip": zipName})
	if err != nil {
		return err
	}
	if _, err = manifestFile.Write(manifest); err != nil {
		return err
	}
	if err = manifestFile.Sync(); err != nil {
		return err
	}
	stored[zipName] = zipFile.Name()
	stored[manifestName] = manifestFile.Name()
	return c.StoreOriginals(ctx, e.User, root, stored)
}
