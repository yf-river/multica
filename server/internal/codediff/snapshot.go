package codediff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options bounds one capture. The defaults are chosen so a repository with an
// installed dependency tree still finishes in about a second: dependency
// directories are described, not enumerated.
type Options struct {
	MaxFiles        int
	MaxFileBytes    int64
	MaxHashBytes    int64
	MaxContentBytes int64
	TimeBudget      time.Duration
	DependencyDirs  []string
	// BuildOutputDirs win over DependencyDirs: a build artefact is a change the
	// user asked to see, even when someone also listed it as a dependency.
	BuildOutputDirs []string
	ExcludePaths    []string
}

func DefaultOptions() Options {
	return Options{
		MaxFiles:        50_000,
		MaxFileBytes:    2 << 20,
		MaxHashBytes:    32 << 20,
		MaxContentBytes: 64 << 20,
		TimeBudget:      10 * time.Second,
		DependencyDirs: []string{
			"node_modules", ".venv", "venv", "vendor", ".pnpm-store",
			".gradle", "__pycache__", ".cache", ".yarn", ".turbo",
		},
	}
}

func (o Options) normalized() Options {
	if o.MaxFiles <= 0 {
		o.MaxFiles = 50_000
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = 2 << 20
	}
	if o.MaxHashBytes <= 0 {
		o.MaxHashBytes = 32 << 20
	}
	if o.MaxContentBytes <= 0 {
		o.MaxContentBytes = 64 << 20
	}
	if o.TimeBudget <= 0 {
		o.TimeBudget = 10 * time.Second
	}
	return o
}

func (o Options) isDependency(name string) bool {
	for _, dir := range o.BuildOutputDirs {
		if name == dir {
			return false
		}
	}
	for _, dir := range o.DependencyDirs {
		if name == dir {
			return true
		}
	}
	return false
}

type fileMeta struct {
	Size     int64
	Mode     uint32
	Lines    int
	Sum      string
	Binary   bool
	Text     bool
	Stored   bool
	Redacted bool
	SizeOnly bool
}

type dirMeta struct {
	Path        string
	Files       int
	Size        int64
	Children    map[string]struct{}
	Approximate bool
}

// Snapshot is the state of one directory at one moment. Content is kept in a
// throwaway store outside the work tree, so a patch can still be rendered after
// the run without the agent's files being read twice into memory.
type Snapshot struct {
	Root      string
	Store     string
	Files     map[string]*fileMeta
	Dirs      map[string]*dirMeta
	Repos     map[string]string // repo root relative to Root -> HEAD commit
	Truncated bool
	Reason    string
	Skipped   []string
}

var errBudget = errors.New("snapshot time budget exceeded")

// dependencyCountBudget bounds the per-directory cost of describing a
// dependency tree. It is deliberately short: the number is context, not data.
const dependencyCountBudget = 150 * time.Millisecond

func TakeSnapshot(ctx context.Context, root string, opts Options) (*Snapshot, error) {
	opts = opts.normalized()
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	store, err := os.MkdirTemp("", "multica-diff-store-*")
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{
		Root:  root,
		Store: store,
		Files: map[string]*fileMeta{},
		Dirs:  map[string]*dirMeta{},
		Repos: map[string]string{},
	}
	// 1. An exclusion that contains the root would exclude everything: the
	//    work directory normally lives under the daemon workspace root it is
	//    asked to avoid walking.
	opts.ExcludePaths = dropAncestors(root, opts.ExcludePaths)
	ctx, cancel := context.WithTimeout(ctx, opts.TimeBudget)
	defer cancel()
	var contentBytes int64
	stop := false
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if stop {
			return fs.SkipAll
		}
		if ctx.Err() != nil {
			stop = true
			snap.Truncated = true
			snap.Reason = "扫描超出时间预算，只记录了已遍历到的内容"
			return fs.SkipAll
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if err != nil {
			// A permission error on one subtree must not lose the rest of the run.
			snap.Skipped = append(snap.Skipped, rel)
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if excluded(path, opts.ExcludePaths) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Name() == ".git" {
			// A linked worktree keeps .git as a file pointing at the main
			// repository; both shapes mark the directory as a repository.
			repoRoot := filepath.Dir(rel)
			if repoRoot == "" {
				repoRoot = "."
			}
			snap.Repos[repoRoot] = ""
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if opts.isDependency(entry.Name()) {
				snap.recordDependency(ctx, path, rel)
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if len(snap.Files) >= opts.MaxFiles {
			stop = true
			snap.Truncated = true
			snap.Reason = fmt.Sprintf("文件超过 %d 个上限，只记录了前 %d 个", opts.MaxFiles, opts.MaxFiles)
			return fs.SkipAll
		}
		meta := snap.readFile(path, rel, entry, opts, &contentBytes)
		if meta != nil {
			snap.Files[rel] = meta
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		snap.Close()
		return nil, walkErr
	}
	for repo := range snap.Repos {
		snap.Repos[repo] = gitHead(ctx, filepath.Join(root, repo))
	}
	return snap, nil
}

// dropAncestors removes any exclusion that contains the root being captured.
func dropAncestors(root string, excludes []string) []string {
	kept := make([]string, 0, len(excludes))
	cleanRoot := filepath.Clean(root)
	for _, prefix := range excludes {
		if prefix == "" {
			continue
		}
		clean := filepath.Clean(prefix)
		if cleanRoot == clean || strings.HasPrefix(cleanRoot, clean+string(filepath.Separator)) {
			continue
		}
		kept = append(kept, prefix)
	}
	return kept
}

func excluded(path string, excludes []string) bool {
	for _, prefix := range excludes {
		if prefix == "" {
			continue
		}
		clean := filepath.Clean(prefix)
		if path == clean || strings.HasPrefix(path, clean+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (s *Snapshot) readFile(path, rel string, entry fs.DirEntry, opts Options, contentBytes *int64) *fileMeta {
	info, err := entry.Info()
	if err != nil {
		return nil
	}
	meta := &fileMeta{Size: info.Size(), Mode: uint32(info.Mode().Perm())}
	meta.Redacted = IsSecretPath(rel)
	if info.Size() > opts.MaxHashBytes {
		// Too large to read on every capture: compare by size alone and say so.
		meta.SizeOnly = true
		if meta.Redacted {
			meta.Text = false
		}
		return meta
	}
	if info.Size() > opts.MaxFileBytes || *contentBytes+info.Size() > opts.MaxContentBytes {
		if data, ok := readHead(path, 8192); ok {
			meta.Binary = isBinary(data)
		}
		meta.Sum = streamSum(path)
		return meta
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return meta
	}
	meta.Sum = sha256Hex(data)
	meta.Lines = countLines(data)
	if isBinary(data) {
		meta.Binary = true
		return meta
	}
	meta.Text = true
	if meta.Redacted {
		return meta
	}
	if err := s.storeContent(meta.Sum, data); err != nil {
		return meta
	}
	meta.Stored = true
	*contentBytes += info.Size()
	return meta
}

func (s *Snapshot) storeContent(sum string, data []byte) error {
	if sum == "" {
		return nil
	}
	target := filepath.Join(s.Store, sum)
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	return os.WriteFile(target, data, 0o600)
}

// Content returns the stored bytes of a file, if the snapshot kept them.
func (s *Snapshot) Content(rel string) (string, bool) {
	meta, ok := s.Files[rel]
	if !ok || !meta.Stored || meta.Sum == "" {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(s.Store, meta.Sum))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (s *Snapshot) recordDependency(ctx context.Context, path, rel string) {
	dir := &dirMeta{Path: rel, Children: map[string]struct{}{}}
	entries, err := os.ReadDir(path)
	if err == nil {
		for _, entry := range entries {
			dir.Children[entry.Name()] = struct{}{}
		}
	}
	// Counting inside a dependency tree is what makes a capture expensive: one
	// installed node_modules holds hundreds of thousands of files. The count is
	// therefore provisional — bounded in time and marked approximate when the
	// bound is hit, because the number only conveys scale.
	deadline := time.Now().Add(dependencyCountBudget)
	countCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	_ = filepath.WalkDir(path, func(child string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if countCtx.Err() != nil {
			dir.Approximate = true
			return fs.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		dir.Files++
		dir.Size += info.Size()
		return nil
	})
	s.Dirs[rel] = dir
}

func (s *Snapshot) Close() {
	if s == nil || s.Store == "" {
		return
	}
	_ = os.RemoveAll(s.Store)
	s.Store = ""
}

func readHead(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := io.ReadFull(f, buf)
	return buf[:n], true
}

func streamSum(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	if data[len(data)-1] != '\n' {
		count++
	}
	return count
}

func isBinary(data []byte) bool {
	limit := min(len(data), 8192)
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

func gitHead(ctx context.Context, dir string) string {
	out, _, err := git(ctx, dir, "", 4096, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// GitCommand exposes the bounded git runner to the capture layer without
// re-implementing timeouts and credential prompts.
func GitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	out, _, err := git(ctx, dir, "", 4<<20, args...)
	return out, err
}
