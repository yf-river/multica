// Package investigation 提供监督运行与评测共用的有界只读调查工具。
package investigation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/redact"
)

const PageSize = 8000

type Root struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	RunID string `json:"run_id,omitempty"`
	Label string `json:"label,omitempty"`
}

type Request struct {
	Root     string `json:"root"`
	Path     string `json:"path,omitempty"`
	Query    string `json:"query,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Revision string `json:"revision,omitempty"`
	Reason   string `json:"reason"`
}

type Result struct {
	Kind        string    `json:"kind"`
	ID          string    `json:"id,omitempty"`
	Root        string    `json:"root"`
	RootSHA256  string    `json:"root_sha256,omitempty"`
	SourceLabel string    `json:"source_label,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	Tool        string    `json:"tool"`
	Path        string    `json:"path,omitempty"`
	Revision    string    `json:"revision,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
	Text        string    `json:"text"`
	Offset      int       `json:"offset"`
	End         int       `json:"end"`
	Length      int       `json:"length"`
	Truncated   bool      `json:"truncated"`
	Error       string    `json:"error,omitempty"`
}

// Session 的根目录由领取批次或冻结评测清单提供，不接受模型补充路径。
type Session struct {
	Roots        []Root
	Calls        int
	Characters   int
	Save         func(context.Context, Request, *Result) error
	ReadMaterial func(context.Context, Request) (Result, error)
}

func hidden(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".git" || part == "node_modules" || part == ".run" || part == ".ssh" || part == ".codex" || part == ".codebuddy" || strings.HasPrefix(part, ".env") || part == "credentials.json" || part == "auth.json" || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") {
			return true
		}
	}
	return false
}

func safePath(name string) bool {
	return name != "" && fs.ValidPath(name) && !hidden(name) && !strings.Contains(name, "\\")
}

func rejectLinks(root *os.Root, name string) error {
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("不通过符号链接读取材料")
		}
	}
	return nil
}

func (s *Session) Call(ctx context.Context, tool string, req Request) (Result, error) {
	out := Result{Kind: "investigation", Root: req.Root, Tool: tool, Path: req.Path, Offset: req.Offset, ObservedAt: time.Now().UTC()}
	if s.Calls >= 40 || s.Characters >= 240000 {
		return out, fmt.Errorf("调查预算已用完；保留未核验项")
	}
	s.Calls++
	if req.Offset < 0 || strings.TrimSpace(req.Reason) == "" {
		return out, fmt.Errorf("需要非负偏移及调查原因")
	}
	if tool == "supervision_material" {
		if req.Root != "materials" {
			return out, fmt.Errorf("历史材料来源必须为materials")
		}
		if s.ReadMaterial == nil {
			return out, fmt.Errorf("本运行没有历史材料读取能力")
		}
		result, err := s.ReadMaterial(ctx, req)
		s.Characters += len([]rune(result.Text))
		return result, err
	}
	var selected *Root
	for i := range s.Roots {
		if s.Roots[i].ID == req.Root {
			selected = &s.Roots[i]
			break
		}
	}
	if selected == nil {
		return out, fmt.Errorf("来源未获本批次授权")
	}
	out.RunID = selected.RunID
	out.SourceLabel = selected.Label
	identity := sha256.Sum256([]byte(selected.Path))
	out.RootSHA256 = hex.EncodeToString(identity[:])
	root, err := os.OpenRoot(selected.Path)
	if err != nil {
		return s.capture(ctx, req, out, fmt.Errorf("来源目录不可用：%w", err))
	}
	defer root.Close()
	var body string
	switch tool {
	case "supervision_read":
		if !safePath(req.Path) {
			err = fmt.Errorf("路径不在可读范围")
			break
		}
		// 拒绝根目录内部的链接别名，避免通过别名读取被排除的凭证文件。
		err = rejectLinks(root, req.Path)
		if err != nil {
			break
		}
		infoBefore, e := root.Lstat(req.Path)
		if e != nil {
			err = e
			break
		}
		if !infoBefore.Mode().IsRegular() {
			err = fmt.Errorf("只读取普通文本文件")
			break
		}
		var f *os.File
		f, err = root.Open(req.Path)
		if err != nil {
			break
		}
		defer f.Close()
		var info fs.FileInfo
		info, err = f.Stat()
		if err != nil {
			break
		}
		if !os.SameFile(infoBefore, info) {
			err = fmt.Errorf("来源在打开期间发生变化，请重新核验")
			break
		}
		if !info.Mode().IsRegular() || info.Size() > 16*1024*1024 {
			err = fmt.Errorf("仅支持16MiB以内的普通文本文件")
			break
		}
		var data []byte
		data, err = io.ReadAll(io.LimitReader(f, 16*1024*1024+1))
		if err != nil {
			break
		}
		if strings.Contains(string(data), "\x00") {
			err = fmt.Errorf("不读取二进制文件")
			break
		}
		body = string(data)
		sum := sha256.Sum256(data)
		out.SHA256 = hex.EncodeToString(sum[:])
	case "supervision_search":
		if req.Path == "" {
			req.Path = "."
		}
		if !safePath(req.Path) || len(req.Query) > 200 {
			err = fmt.Errorf("搜索范围或查询无效")
			break
		}
		if err = rejectLinks(root, req.Path); err != nil {
			break
		}
		var lines []string
		visited := 0
		scanned := int64(0)
		err = fs.WalkDir(root.FS(), req.Path, func(name string, entry fs.DirEntry, e error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e != nil {
				return e
			}
			if hidden(name) {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			visited++
			if visited > 2000 || scanned >= 16*1024*1024 || len(lines) >= 200 {
				out.Truncated = true
				return fs.SkipAll
			}
			if req.Query == "" {
				lines = append(lines, name)
				return nil
			}
			info, e := entry.Info()
			if e != nil {
				out.Truncated = true
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			remaining := 16*1024*1024 - scanned
			if info.Size() > remaining {
				out.Truncated = true
				return nil
			}
			f, e := root.Open(name)
			if e != nil {
				out.Truncated = true
				return nil
			}
			defer f.Close()
			opened, e := f.Stat()
			if e != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
				out.Truncated = true
				return nil
			}
			data, e := io.ReadAll(io.LimitReader(f, remaining+1))
			if int64(len(data)) > remaining {
				data = data[:remaining]
				out.Truncated = true
			}
			scanned += int64(len(data))
			if e != nil {
				out.Truncated = true
				return nil
			}
			if strings.Contains(string(data), "\x00") {
				return nil
			}
			for n, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, req.Query) {
					r := []rune(line)
					if len(r) > 500 {
						start := max(0, utf8.RuneCountInString(line[:strings.Index(line, req.Query)])-200)
						line = string(r[start:min(start+500, len(r))])
						out.Truncated = true
					}
					lines = append(lines, fmt.Sprintf("%s:%d:%s", name, n+1, line))
					if len(lines) >= 200 {
						out.Truncated = true
						return fs.SkipAll
					}
				}
			}
			return nil
		})
		body = strings.Join(lines, "\n")
	case "supervision_git":
		body, out.Revision, err = readGit(ctx, selected.Path, req)
	default:
		err = fmt.Errorf("不支持的只读工具")
	}
	if err != nil {
		out.Error = err.Error()
	} else {
		if out.SHA256 == "" {
			digest := sha256.Sum256([]byte(body))
			out.SHA256 = hex.EncodeToString(digest[:])
		}
		runes := []rune(body)
		out.Length = len(runes)
		if req.Offset > len(runes) {
			req.Offset = len(runes)
			out.Offset = req.Offset
		}
		end := min(req.Offset+PageSize, len(runes))
		out.Text = redact.Text(string(runes[req.Offset:end]))
		out.End = end
		out.Truncated = out.Truncated || end < len(runes)
		s.Characters += len([]rune(out.Text))
	}
	return s.capture(ctx, req, out, err)
}

func (s *Session) capture(ctx context.Context, req Request, out Result, err error) (Result, error) {
	if err != nil {
		out.Error = err.Error()
	}
	if s.Save != nil {
		if saveErr := s.Save(ctx, req, &out); saveErr != nil {
			out.Text = ""
			return out, fmt.Errorf("调查证据保存失败：%w", saveErr)
		}
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

// Git 参数均由工具生成；禁用配置、外部 diff、textconv、分页器和网络对象补取。
func readGit(ctx context.Context, root string, req Request) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		argv := append([]string{"--no-pager", "--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "diff.autoRefreshIndex=false", "-c", "diff.external=", "-c", "core.attributesFile=/dev/null"}, args...)
		cmd := exec.CommandContext(ctx, "git", argv...)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C"}
		pipe, e := cmd.StdoutPipe()
		if e != nil {
			return "", e
		}
		if e = cmd.Start(); e != nil {
			return "", e
		}
		b, e := io.ReadAll(io.LimitReader(pipe, 1024*1024+1))
		if len(b) > 1024*1024 {
			_ = cmd.Process.Kill()
		}
		wait := cmd.Wait()
		if e != nil {
			return "", e
		}
		if len(b) > 1024*1024 {
			return "", fmt.Errorf("Git结果超过1MiB，请缩小文件范围")
		}
		if wait != nil {
			return "", fmt.Errorf("Git只读查询失败：%w", wait)
		}
		return string(b), nil
	}
	head, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", "", err
	}
	head = strings.TrimSpace(head)
	if req.Path != "" && !safePath(req.Path) {
		return "", head, fmt.Errorf("Git路径无效")
	}
	switch req.Query {
	case "status":
		body, e := run("status", "--porcelain=v1", "--untracked-files=no")
		return body, head, e
	case "diff":
		if req.Path == "" {
			return "", head, fmt.Errorf("差异查询须指定文件")
		}
		body, e := run("diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "HEAD", "--", req.Path)
		return body, head, e
	case "history":
		if req.Path == "" {
			return "", head, fmt.Errorf("历史查询须指定文件")
		}
		body, e := run("log", "-10", "--format=%H %cI %s", "--", req.Path)
		return body, head, e
	case "show":
		if len(req.Revision) != 40 || strings.Trim(req.Revision, "0123456789abcdef") != "" || req.Path == "" {
			return "", head, fmt.Errorf("读取历史源码须提供完整提交和文件路径")
		}
		body, e := run("show", "--no-ext-diff", "--no-textconv", req.Revision+":"+path.Clean(req.Path))
		return body, req.Revision, e
	default:
		return "", head, fmt.Errorf("Git操作仅支持status、diff、history和show")
	}
}

func (r Result) JSON() string { b, _ := json.Marshal(r); return string(b) }
