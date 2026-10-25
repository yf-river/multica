package execenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 执行实际生成的 shell 片段，失败保留正文与退出码，成功才清理。
func TestReplyCookbookPreservesFailedPost(t *testing.T) {
	old := runtimeGOOS
	runtimeGOOS = "linux"
	defer func() { runtimeGOOS = old }()
	rendered := buildCommentReplyInstructionsSlim("codex", "issue", "comment", false)
	var line string
	for _, candidate := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(candidate, "    multica issue comment add ") {
			line = strings.TrimSpace(candidate)
			break
		}
	}
	if line == "" {
		t.Fatal("missing reply command")
	}
	for _, failed := range []bool{true, false} {
		dir := t.TempDir()
		script := "#!/bin/sh\nexit 0\n"
		if failed {
			script = "#!/bin/sh\nexit 23\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "multica"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "reply.md"), []byte("验收结果"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", line)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		err := cmd.Run()
		_, statErr := os.Stat(filepath.Join(dir, "reply.md"))
		if failed {
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 23 || statErr != nil {
				t.Fatalf("failed post: err=%v, body=%v", err, statErr)
			}
		} else if err != nil || !os.IsNotExist(statErr) {
			t.Fatalf("successful post: err=%v, body=%v", err, statErr)
		}
	}
}
