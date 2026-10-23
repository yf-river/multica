package codediff

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// baselineFile is one path as it existed at the run's starting point: either in
// the file snapshot taken before the agent started, or in the git tree that was
// checked out during the run.
type baselineFile struct {
	Sum     string
	Size    int64
	Content string
	HasBody bool
	Binary  bool
}

// BaseRef answers "what did this repository look like before the run?". The
// run-start HEAD is authoritative when the repository already existed; a
// repository that appeared mid-run (multica repo checkout, git clone, a new
// worktree) falls back to its upstream, which is exactly the commit it was
// created from.
func BaseRef(ctx context.Context, repo, beforeHead string) (ref, source string) {
	if beforeHead != "" {
		return beforeHead, "run-start"
	}
	if out, err := GitCommand(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		if upstream := strings.TrimSpace(out); upstream != "" {
			if _, err := GitCommand(ctx, repo, "rev-parse", "--verify", "--quiet", upstream+"^{commit}"); err == nil {
				return upstream, "upstream"
			}
		}
	}
	if _, err := GitCommand(ctx, repo, "rev-parse", "--verify", "--quiet", "origin/HEAD^{commit}"); err == nil {
		return "origin/HEAD", "origin-head"
	}
	return "", "none"
}

// TreeBaseline lists the files of a commit and loads the bodies a patch will
// need. One `git cat-file --batch` process serves every body, so a checked-out
// repository costs a single extra git invocation rather than one per file.
func TreeBaseline(ctx context.Context, repo, ref string, opts Options) (map[string]*baselineFile, error) {
	opts = opts.normalized()
	out, err := GitCommand(ctx, repo, "ls-tree", "-r", "-l", "-z", ref)
	if err != nil {
		return nil, err
	}
	files := map[string]*baselineFile{}
	var wanted []blobRef
	for _, entry := range strings.Split(out, "\x00") {
		if entry == "" {
			continue
		}
		// <mode> SP <type> SP <object> SP <size> TAB <path>
		tab := strings.IndexByte(entry, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(entry[:tab])
		if len(fields) != 4 || fields[1] != "blob" {
			continue
		}
		size, _ := strconv.ParseInt(fields[3], 10, 64)
		path := entry[tab+1:]
		files[path] = &baselineFile{Size: size}
		if size <= opts.MaxFileBytes {
			wanted = append(wanted, blobRef{path: path, sha: fields[2], size: size})
		}
	}
	if len(wanted) == 0 {
		return files, nil
	}
	bodies, err := readBlobs(ctx, repo, wanted)
	if err != nil {
		// A missing body only costs the patch, never the file listing.
		return files, nil
	}
	var total int64
	for path, body := range bodies {
		file := files[path]
		if file == nil {
			continue
		}
		if total+int64(len(body)) > opts.MaxContentBytes {
			continue
		}
		total += int64(len(body))
		file.Content = string(body)
		file.HasBody = true
		file.Binary = isBinary(body)
		file.Sum = sha256Hex(body)
	}
	return files, nil
}

type blobRef struct {
	path string
	sha  string
	size int64
}

func readBlobs(ctx context.Context, repo string, wanted []blobRef) (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = repo
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	var input bytes.Buffer
	bySHA := map[string]string{}
	for _, item := range wanted {
		input.WriteString(item.sha)
		input.WriteByte('\n')
		bySHA[item.sha] = item.path
	}
	cmd.Stdin = &input
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(stdout, 1<<20)
	bodies := map[string][]byte{}
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		fields := strings.Fields(strings.TrimSpace(header))
		if len(fields) == 2 && fields[1] == "missing" {
			continue
		}
		if len(fields) != 3 {
			continue
		}
		size, convErr := strconv.Atoi(fields[2])
		if convErr != nil || size < 0 {
			continue
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(reader, body); err != nil {
			break
		}
		// Trailing newline after each object body.
		if _, err := reader.ReadByte(); err != nil {
			break
		}
		if path, ok := bySHA[fields[0]]; ok {
			bodies[path] = body
		}
	}
	_ = cmd.Wait()
	return bodies, nil
}

// WorktreeStart returns the commit a worktree's branch was created from and
// when. The branch reflog records both: its oldest entry is the creation, whose
// commit is the base the worktree started at.
func WorktreeStart(ctx context.Context, worktree string) (base string, createdAt time.Time, branch string) {
	out, err := GitCommand(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", time.Time{}, ""
	}
	branch = strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return "", time.Time{}, ""
	}
	log, err := GitCommand(ctx, worktree, "reflog", "show", "--date=unix", "--format=%H%x00%ct", branch)
	if err != nil {
		return "", time.Time{}, branch
	}
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return "", time.Time{}, branch
	}
	fields := strings.Split(lines[len(lines)-1], "\x00")
	if len(fields) < 2 {
		return "", time.Time{}, branch
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
	if err != nil {
		return strings.TrimSpace(fields[0]), time.Time{}, branch
	}
	return strings.TrimSpace(fields[0]), time.Unix(seconds, 0), branch
}

// MergeBase is the fallback baseline: the last commit a branch shared with the
// repository's starting point.
func MergeBase(ctx context.Context, repo, a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	out, err := GitCommand(ctx, repo, "merge-base", a, b)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CommitCount counts commits reachable from head but not from base.
func CommitCount(ctx context.Context, repo, base, head string) int {
	if base == "" || head == "" {
		return 0
	}
	out, err := GitCommand(ctx, repo, "rev-list", "--count", base+".."+head)
	if err != nil {
		return 0
	}
	count, _ := strconv.Atoi(strings.TrimSpace(out))
	return count
}

// boundedBuffer prevents large repository output from exhausting memory.
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

// git runs one bounded git command with prompts and optional locks disabled, so
// a capture can never hang on credentials or fight the user's own git process.
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
		return "", false, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out.buffer.String(), out.exceeded, nil
}
