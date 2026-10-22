package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/upstream"
)

// 镜像目录必须只由仓库和分支决定：按 run 建目录会让每次同步都全量克隆。
func TestSyncMirrorKeyStablePerRepository(t *testing.T) {
	base := upstream.Config{UpstreamURL: "https://example.test/upstream.git", UpstreamBranch: "main"}
	first := syncMirrorKey(base)
	if first != syncMirrorKey(base) {
		t.Fatalf("同一仓库两次取键不一致: %q", first)
	}
	other := base
	other.UpstreamBranch = "release"
	if first == syncMirrorKey(other) {
		t.Fatalf("不同分支共用镜像键: %q", first)
	}
	other = base
	other.UpstreamURL = "https://example.test/fork.git"
	if first == syncMirrorKey(other) {
		t.Fatalf("不同仓库共用镜像键: %q", first)
	}
}
