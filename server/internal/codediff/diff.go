// Package codediff captures immutable, bounded changes between run boundaries.
package codediff

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const MaxFiles = 100
const MaxPatchBytes = 128 * 1024
const MaxTotalBytes = 1024 * 1024

type File struct {
	Path      string `json:"path"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Patch     string `json:"patch"`
}
type Diff struct {
	Before      string `json:"before"`
	After       string `json:"after"`
	Files       []File `json:"files"`
	Truncated   bool   `json:"truncated"`
	Unavailable bool   `json:"unavailable"`
}

// boundedBuffer prevents large files or repository output from exhausting memory.
type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n > b.limit-b.buffer.Len() {
		b.exceeded = true
		p = p[:b.limit-b.buffer.Len()]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
func git(ctx context.Context, dir, index string, limit int, args ...string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	out := &boundedBuffer{limit: limit}
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("git snapshot: %w", err)
	}
	return out.buffer.String(), out.exceeded, nil
}

// Snapshot uses a private index, preserving the user's staging area and HEAD.
// Only the repository root is accepted: a nested non-repository task must not
// accidentally capture unrelated files from an enclosing checkout.
func Snapshot(ctx context.Context, dir string) (string, error) {
	root, _, err := git(ctx, dir, "", 8192, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	abs, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if filepath.Clean(strings.TrimSpace(root)) != filepath.Clean(abs) {
		return "", fmt.Errorf("task directory is not repository root")
	}
	f, err := os.CreateTemp("", "multica-diff-index-*")
	if err != nil {
		return "", err
	}
	index := f.Name()
	_ = f.Close()
	_ = os.Remove(index)
	defer os.Remove(index)
	defer os.Remove(index + ".lock")
	if _, _, err = git(ctx, dir, index, 8192, "read-tree", "HEAD"); err != nil {
		if _, _, err = git(ctx, dir, index, 8192, "read-tree", "--empty"); err != nil {
			return "", err
		}
	}
	if _, _, err = git(ctx, dir, index, 8192, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	tree, _, err := git(ctx, dir, index, 8192, "write-tree")
	return strings.TrimSpace(tree), err
}

func Collect(ctx context.Context, dir, before string) (*Diff, error) {
	after, err := Snapshot(ctx, dir)
	if err != nil {
		return nil, err
	}
	d := &Diff{Before: before, After: after, Files: []File{}}
	stats, clipped, err := git(ctx, dir, "", MaxTotalBytes, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--numstat", "-z", before, after, "--")
	if err != nil {
		return nil, err
	}
	d.Truncated = clipped
	total := 0
	for _, stat := range strings.Split(stats, "\x00") {
		if stat == "" {
			continue
		}
		fields := strings.SplitN(stat, "\t", 3)
		if len(fields) != 3 {
			d.Truncated = true
			continue
		}
		if len(d.Files) == MaxFiles {
			d.Truncated = true
			break
		}
		f := File{Path: fields[2], Binary: fields[0] == "-"}
		f.Added, _ = strconv.Atoi(fields[0])
		f.Deleted, _ = strconv.Atoi(fields[1])
		if !f.Binary && total < MaxTotalBytes {
			limit := min(MaxPatchBytes, MaxTotalBytes-total)
			f.Patch, f.Truncated, err = git(ctx, dir, "", limit, "--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", before, after, "--", f.Path)
			if err != nil {
				return nil, err
			}
			total += len(f.Patch)
		} else if !f.Binary {
			f.Truncated = true
		}
		d.Truncated = d.Truncated || f.Truncated
		d.Files = append(d.Files, f)
	}
	return d, nil
}
