// Package codediff captures immutable, bounded changes between run boundaries.
//
// It answers one question: "what did this run change on disk?" A run may touch
// its own work directory and, for repository-backed tasks, worktrees created
// outside it. Both are reported as roots so the UI can keep them apart instead
// of merging unrelated trees into one list.
package codediff

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxPatchBytes      = 256 * 1024
	MaxTotalPatchBytes = 2 << 20
	MaxFilesPerRoot    = 300
	MaxRoots           = 4
)

// File is one changed path. Text bodies become a unified patch; everything else
// still shows up with its size and line counts.
type File struct {
	Path        string `json:"path"`
	Added       int    `json:"added"`
	Deleted     int    `json:"deleted"`
	Binary      bool   `json:"binary"`
	Truncated   bool   `json:"truncated"`
	Patch       string `json:"patch"`
	Change      string `json:"change,omitempty"` // added | modified | deleted
	Size        int64  `json:"size,omitempty"`
	BeforeSize  int64  `json:"before_size,omitempty"`
	Lines       int    `json:"lines,omitempty"`
	BeforeLines int    `json:"before_lines,omitempty"`
	Redacted    bool   `json:"redacted,omitempty"`
}

// DirSummary describes a dependency directory without listing its files.
type DirSummary struct {
	Path            string   `json:"path"`
	Files           int      `json:"files"`
	Size            int64    `json:"size"`
	Approximate     bool     `json:"approximate,omitempty"`
	BeforeFiles     int      `json:"before_files,omitempty"`
	BeforeSize      int64    `json:"before_size,omitempty"`
	AddedChildren   []string `json:"added_children,omitempty"`
	RemovedChildren []string `json:"removed_children,omitempty"`
	Added           bool     `json:"added,omitempty"`
	Removed         bool     `json:"removed,omitempty"`
}

// Root is one independent tree the run touched: the task work directory, a
// worktree created outside it, or a user directory used in place.
type Root struct {
	Path        string       `json:"path"`
	Kind        string       `json:"kind"` // workdir | external | local_directory
	Label       string       `json:"label"`
	Branch      string       `json:"branch,omitempty"`
	Base        string       `json:"base,omitempty"`
	BaseSource  string       `json:"base_source,omitempty"`
	Commits     int          `json:"commits,omitempty"`
	Attribution string       `json:"attribution,omitempty"` // task | unassigned
	Files       []File       `json:"files"`
	Dirs        []DirSummary `json:"dirs,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
	Unavailable bool         `json:"unavailable,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	// 修改 1. Paths inside this root that could not be read. Without them a
	//         permission error looked like "nothing changed here", which is the
	//         same silent blank the report card is supposed to rule out.
	Skipped []string `json:"skipped,omitempty"`
}

// Diff is the value stored on a task result as code_diff. Version 2 adds roots;
// the flat fields stay populated so a client that predates them still renders
// the work-directory part instead of nothing.
type Diff struct {
	Version     int    `json:"version"`
	Before      string `json:"before"`
	After       string `json:"after"`
	Files       []File `json:"files"`
	Truncated   bool   `json:"truncated"`
	Unavailable bool   `json:"unavailable"`
	Reason      string `json:"reason,omitempty"`
	Roots       []Root `json:"roots,omitempty"`
	// 修改 2. Run-level notes about what the card is not showing (a worktree
	//         dropped by the root cap). Root-scoped gaps stay on their root.
	Notes []string `json:"notes,omitempty"`
}

// RootSpec is one tree to capture. Before is the snapshot taken when the run
// started; a nil Before means the tree did not exist then and everything in it
// is new.
type RootSpec struct {
	Path        string
	Kind        string
	Label       string
	Before      *Snapshot
	Attribution string
	// BaseOverride pins the baseline commit, for a worktree whose branch reflog
	// no longer points at the commit it was created from.
	BaseOverride string
	BaseSource   string
	Commits      int
}

// BuildRoot compares one tree against its baseline.
func BuildRoot(ctx context.Context, spec RootSpec, opts Options) Root {
	opts = opts.normalized()
	root := Root{
		Path:        spec.Path,
		Kind:        spec.Kind,
		Label:       spec.Label,
		Attribution: spec.Attribution,
		Base:        spec.BaseOverride,
		BaseSource:  spec.BaseSource,
		Commits:     spec.Commits,
		Files:       []File{},
	}
	after, err := TakeSnapshot(ctx, spec.Path, opts)
	if err != nil {
		root.Unavailable = true
		root.Reason = fmt.Sprintf("无法读取目录：%v", err)
		return root
	}
	defer after.Close()
	// 修改 3. Carry unreadable paths up to the card: skipping them silently made
	//         a partial capture indistinguishable from "this run changed nothing".
	root.Skipped = trimSkipped(after.Skipped)

	repoRoots := map[string]string{} // repo root (relative, "." for self) -> head
	for rel, head := range after.Repos {
		repoRoots[rel] = head
	}
	if spec.Before != nil {
		for rel := range spec.Before.Repos {
			if _, ok := repoRoots[rel]; !ok {
				repoRoots[rel] = ""
			}
		}
	}
	baselines := map[string]map[string]*baselineFile{}
	for rel := range repoRoots {
		absRepo := spec.Path
		if rel != "." {
			absRepo = filepath.Join(spec.Path, rel)
		}
		beforeHead := ""
		if spec.Before != nil {
			beforeHead = spec.Before.Repos[rel]
		}
		base := spec.BaseOverride
		source := spec.BaseSource
		if base == "" {
			base, source = BaseRef(ctx, absRepo, beforeHead)
		}
		if rel == "." {
			root.Base = base
			root.BaseSource = source
			root.Branch = gitBranch(ctx, absRepo)
		}
		if base == "" {
			continue
		}
		files, err := TreeBaseline(ctx, absRepo, base, opts)
		if err != nil {
			continue
		}
		baselines[rel] = files
	}

	paths := map[string]struct{}{}
	for path := range after.Files {
		paths[path] = struct{}{}
	}
	if spec.Before != nil {
		for path := range spec.Before.Files {
			paths[path] = struct{}{}
		}
	}
	for _, files := range baselines {
		for path := range files {
			paths[path] = struct{}{}
		}
	}
	// A baseline path is relative to its repository, so re-prefix it before the
	// union so a nested checkout compares against its own tree.
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	listed := 0
	totalPatch := 0
	for _, path := range ordered {
		repoRel, inRepo := owningRepo(path, repoRoots)
		var baseFile *baselineFile
		if inRepo {
			if files, ok := baselines[repoRel]; ok {
				inner := path
				if repoRel != "." {
					inner = strings.TrimPrefix(path, repoRel+"/")
				}
				baseFile = files[inner]
			}
		}
		var beforeMeta *fileMeta
		var beforeContent string
		beforeHasContent := false
		if spec.Before != nil {
			beforeMeta = spec.Before.Files[path]
			if beforeMeta != nil {
				beforeContent, beforeHasContent = spec.Before.Content(path)
			}
		}
		afterMeta := after.Files[path]
		if beforeMeta == nil && baseFile == nil && afterMeta == nil {
			continue
		}
		change := classify(beforeMeta, baseFile, afterMeta)
		if change == "" {
			continue
		}
		if listed >= MaxFilesPerRoot {
			root.Truncated = true
			break
		}
		file := buildFile(path, change, beforeMeta, baseFile, afterMeta, beforeContent, beforeHasContent, after, opts, &totalPatch)
		if file == nil {
			continue
		}
		root.Files = append(root.Files, *file)
		listed++
	}
	root.Dirs = diffDirs(spec.Before, after)
	if after.Truncated {
		root.Truncated = true
		if root.Reason == "" {
			root.Reason = after.Reason
		}
	}
	if len(root.Files) == 0 && len(root.Dirs) == 0 && !root.Truncated && root.Reason == "" {
		root.Reason = ""
	}
	return root
}

// 修改 4. A pathological tree can skip thousands of paths; the list is context,
//
//	not data, so it is capped like every other field on the card.
const maxSkippedPaths = 20

func trimSkipped(paths []string) []string {
	if len(paths) <= maxSkippedPaths {
		return paths
	}
	return append(paths[:maxSkippedPaths:maxSkippedPaths], fmt.Sprintf("…等 %d 个路径", len(paths)))
}

func owningRepo(path string, repos map[string]string) (string, bool) {
	best := ""
	found := false
	for rel := range repos {
		if rel == "." {
			if !found {
				best, found = ".", true
			}
			continue
		}
		if path == rel || strings.HasPrefix(path, rel+"/") {
			if len(rel) > len(best) || best == "." {
				best, found = rel, true
			}
		}
	}
	return best, found
}

func classify(beforeMeta *fileMeta, baseFile *baselineFile, afterMeta *fileMeta) string {
	beforeExists := beforeMeta != nil || baseFile != nil
	switch {
	case afterMeta == nil:
		if !beforeExists {
			return ""
		}
		return "deleted"
	case !beforeExists:
		return "added"
	}
	if afterMeta.SizeOnly {
		// Too large to hash: only a size delta is trustworthy.
		beforeSize := int64(0)
		if beforeMeta != nil {
			beforeSize = beforeMeta.Size
		} else if baseFile != nil {
			beforeSize = baseFile.Size
		}
		if beforeSize == afterMeta.Size {
			return ""
		}
		return "modified"
	}
	if beforeMeta != nil && beforeMeta.Sum != "" && afterMeta.Sum == beforeMeta.Sum {
		return ""
	}
	if beforeMeta == nil && baseFile != nil && baseFile.Sum != "" && afterMeta.Sum == baseFile.Sum {
		return ""
	}
	return "modified"
}

func buildFile(path, change string, beforeMeta *fileMeta, baseFile *baselineFile, afterMeta *fileMeta,
	beforeContent string, beforeHasContent bool, after *Snapshot, opts Options, totalPatch *int) *File {
	file := &File{Path: path, Change: change}
	var beforeSize int64
	var beforeLines int
	if beforeMeta != nil {
		beforeSize, beforeLines = beforeMeta.Size, beforeMeta.Lines
	} else if baseFile != nil {
		beforeSize = baseFile.Size
	}
	if afterMeta != nil {
		file.Size, file.Lines = afterMeta.Size, afterMeta.Lines
	}
	file.BeforeSize, file.BeforeLines = beforeSize, beforeLines
	binary := (afterMeta != nil && afterMeta.Binary) || (beforeMeta != nil && beforeMeta.Binary) ||
		(baseFile != nil && baseFile.Binary)
	if !afterHasContent(afterMeta) && !beforeHasContent && (baseFile == nil || !baseFile.HasBody) && !binary {
		binary = false
	}
	file.Binary = binary
	if afterMeta != nil && afterMeta.Redacted {
		file.Redacted = true
	}
	if afterMeta == nil {
		if baseFile != nil && IsSecretPath(path) {
			file.Redacted = true
		}
	}
	if file.Redacted {
		return file
	}
	if binary {
		return file
	}
	afterContent, hasAfterContent := "", false
	if afterMeta != nil {
		afterContent, hasAfterContent = after.Content(path)
	}
	if !hasAfterContent && afterMeta != nil && afterMeta.Lines == 0 && afterMeta.Size > 0 {
		return file
	}
	if !beforeHasContent && baseFile != nil && baseFile.HasBody {
		beforeContent, beforeHasContent = baseFile.Content, true
	}
	if !hasAfterContent && !beforeHasContent {
		return file
	}
	if change != "deleted" && !hasAfterContent {
		return file
	}
	patch, added, deleted := buildPatch(path, beforeContent, afterContent)
	if added > 0 || deleted > 0 || patch != "" {
		file.Added, file.Deleted = added, deleted
	}
	if patch == "" {
		return file
	}
	patch = redactPatch(patch, &file.Redacted)
	remaining := MaxTotalPatchBytes - *totalPatch
	limit := min(MaxPatchBytes, remaining)
	if limit <= 0 {
		file.Truncated = true
		return file
	}
	if len(patch) > limit {
		patch = patch[:limit]
		file.Truncated = true
	}
	*totalPatch += len(patch)
	file.Patch = patch
	return file
}

func afterHasContent(meta *fileMeta) bool {
	return meta != nil && meta.Stored
}

func redactPatch(patch string, redacted *bool) string {
	lines := strings.Split(patch, "\n")
	for index, line := range lines {
		if line == "" || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "@@") {
			continue
		}
		if masked, hit := RedactLine(line); hit {
			lines[index] = string(line[0]) + masked
			*redacted = true
		}
	}
	return strings.Join(lines, "\n")
}

func diffDirs(before, after *Snapshot) []DirSummary {
	summaries := []DirSummary{}
	seen := map[string]struct{}{}
	if after != nil {
		for path, dir := range after.Dirs {
			summary := DirSummary{Path: path, Files: dir.Files, Size: dir.Size, Approximate: dir.Approximate}
			if before != nil {
				if prior, ok := before.Dirs[path]; ok {
					summary.BeforeFiles, summary.BeforeSize = prior.Files, prior.Size
					for child := range dir.Children {
						if _, existed := prior.Children[child]; !existed {
							summary.AddedChildren = append(summary.AddedChildren, child)
						}
					}
					for child := range prior.Children {
						if _, still := dir.Children[child]; !still {
							summary.RemovedChildren = append(summary.RemovedChildren, child)
						}
					}
					if summary.BeforeFiles == summary.Files && summary.BeforeSize == summary.Size &&
						len(summary.AddedChildren) == 0 && len(summary.RemovedChildren) == 0 {
						continue
					}
				} else {
					summary.Added = true
				}
			} else {
				summary.Added = true
			}
			sort.Strings(summary.AddedChildren)
			sort.Strings(summary.RemovedChildren)
			summaries = append(summaries, summary)
			seen[path] = struct{}{}
		}
	}
	if before != nil {
		for path, dir := range before.Dirs {
			if _, ok := seen[path]; ok {
				continue
			}
			summaries = append(summaries, DirSummary{Path: path, BeforeFiles: dir.Files, BeforeSize: dir.Size, Removed: true})
		}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Path < summaries[j].Path })
	return summaries
}

func gitBranch(ctx context.Context, dir string) string {
	out, err := GitCommand(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(out)
	if branch == "HEAD" {
		return ""
	}
	return branch
}

// Assembly turns roots into the stored value. Flat fields mirror the work
// directory root so older clients keep showing something real.
func Assembly(roots []Root, before, after *Snapshot) *Diff {
	diff := &Diff{Version: 2, Files: []File{}, Roots: roots}
	if before != nil {
		diff.Before = before.digest()
	}
	if after != nil {
		diff.After = after.digest()
	}
	for _, root := range roots {
		if root.Truncated {
			diff.Truncated = true
		}
		if len(diff.Files) >= MaxFilesPerRoot {
			continue
		}
		diff.Files = append(diff.Files, root.Files...)
	}
	if len(diff.Files) > MaxFilesPerRoot {
		diff.Files = diff.Files[:MaxFilesPerRoot]
		diff.Truncated = true
	}
	// 2. When nothing could be captured at all, say so at the top level: an
	//    empty card with no explanation is the original GONG-57 symptom.
	if len(roots) > 0 {
		unavailable := true
		for _, root := range roots {
			if !root.Unavailable {
				unavailable = false
				break
			}
		}
		if unavailable {
			diff.Unavailable = true
			diff.Reason = roots[0].Reason
		}
	}
	return diff
}

// Unavailable is the value recorded when capture itself failed: the card says so
// instead of pretending the run changed nothing.
func Unavailable(reason string) *Diff {
	return &Diff{Version: 2, Files: []File{}, Unavailable: true, Reason: reason}
}

func (s *Snapshot) digest() string {
	if s == nil {
		return ""
	}
	keys := make([]string, 0, len(s.Files))
	for path, meta := range s.Files {
		keys = append(keys, path+":"+meta.Sum)
	}
	sort.Strings(keys)
	return sha256Hex([]byte(strings.Join(keys, "\n")))[:12]
}
