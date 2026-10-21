package investigation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

// 游标只指定同一授权搜索中的续查位置，不提供新的路径权限。
type searchCursor struct {
	Scope string `json:"scope"`
	File  string `json:"file"`
	Line  int    `json:"line"`
}

func search(ctx context.Context, root *os.Root, selected Root, req Request, out *Result) (string, error) {
	if req.Path == "" {
		req.Path = "."
	}
	if !safePath(req.Path) || len(req.Query) > 200 {
		return "", fmt.Errorf("搜索范围或查询无效")
	}
	if err := rejectLinks(root, req.Path); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(selected.Path + "\n" + req.Root + "\n" + req.Path + "\n" + req.Query))
	scope := hex.EncodeToString(sum[:])
	cur := searchCursor{}
	if req.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if len(req.Cursor) > 8192 || err != nil || json.Unmarshal(raw, &cur) != nil || cur.Scope != scope || !safePath(cur.File) || cur.Line < 0 {
			return "", fmt.Errorf("续查游标与搜索范围不符，请保持root、path和query不变")
		}
	}
	resume := cur.File == ""
	var lines []string
	visited := 0
	var scanned int64
	next := func(file string, line int) error {
		raw, _ := json.Marshal(searchCursor{Scope: scope, File: file, Line: line})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		out.ScopeTruncated = true
		return fs.SkipAll
	}
	skipped := func(name, reason string) {
		out.ScopeTruncated = true
		if len(out.Skipped) < 30 {
			out.Skipped = append(out.Skipped, name+": "+reason)
		}
	}
	err := fs.WalkDir(root.FS(), req.Path, func(name string, entry fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil {
			skipped(name, e.Error())
			return nil
		}
		if hidden(name) || (name != "." && !selected.Allows(name, "supervision_read")) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			relative := strings.TrimPrefix(name, strings.TrimSuffix(req.Path, ".")+"/")
			if req.Path == "." {
				relative = name
			}
			if name != req.Path && !strings.Contains(relative, "/") && len(out.Directories) < 30 {
				out.Directories = append(out.Directories, name)
			}
			return nil
		}
		if !resume {
			if name != cur.File {
				return nil
			}
			resume = true
		}
		if visited >= 2000 || scanned >= 16*1024*1024 || len(lines) >= 200 {
			return next(name, 0)
		}
		visited++
		if req.Query == "" {
			lines = append(lines, name)
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			skipped(name, e.Error())
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > 16*1024*1024 {
			skipped(name, "超过16MiB，请缩小来源或分拆原始材料")
			return nil
		}
		if scanned+info.Size() > 16*1024*1024 {
			return next(name, 0)
		}
		f, e := root.Open(name)
		if e != nil {
			skipped(name, e.Error())
			return nil
		}
		opened, e := f.Stat()
		if e != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			f.Close()
			skipped(name, "打开期间来源发生变化")
			return nil
		}
		data, e := io.ReadAll(io.LimitReader(f, 16*1024*1024+1))
		f.Close()
		scanned += int64(len(data))
		if e != nil || len(data) > 16*1024*1024 {
			skipped(name, "读取不完整")
			return nil
		}
		if strings.Contains(string(data), "\x00") {
			return nil
		}
		lineOffset := 0
		for n, line := range strings.Split(string(data), "\n") {
			sourceOffset := lineOffset
			lineOffset += utf8.RuneCountInString(line) + 1
			if name == cur.File && n < cur.Line {
				continue
			}
			if len(lines) >= 200 {
				return next(name, n)
			}
			if !strings.Contains(line, req.Query) {
				continue
			}
			r := []rune(line)
			if len(r) > 500 {
				start := max(0, utf8.RuneCountInString(line[:strings.Index(line, req.Query)])-200)
				sourceOffset += start
				line = string(r[start:min(start+500, len(r))])
				out.ContentTruncated = true
			}
			lines = append(lines, fmt.Sprintf("%s:%d: [source_offset=%d] %s", name, n+1, sourceOffset, line))
		}
		return nil
	})
	if err == nil && !resume {
		err = fmt.Errorf("续查文件已不存在，来源发生变化；请从范围起点重新搜索")
	}
	return strings.Join(lines, "\n"), err
}
