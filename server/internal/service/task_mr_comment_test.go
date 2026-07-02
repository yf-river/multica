package service

import "testing"

func TestParseGongfengMRRefsFromComment(t *testing.T) {
	content := "05-验证测试完成。\n\n" +
		"**MR 已创建**：\n" +
		"- MR !81：https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/81\n" +
		"- 源分支：`agent/password-strength-password-strength-1782968998772`\n" +
		"- 目标分支：`v5.0.0_dev_sop`\n"

	refs := parseGongfengMRRefsFromComment(content)
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1: %+v", len(refs), refs)
	}
	ref := refs[0]
	if ref.ProjectPath != "ChainWeaver/ida/user-center" {
		t.Fatalf("project path = %q", ref.ProjectPath)
	}
	if ref.Number != 81 {
		t.Fatalf("number = %d", ref.Number)
	}
	if ref.HTMLURL != "https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/81" {
		t.Fatalf("html url = %q", ref.HTMLURL)
	}
	if ref.SourceBranch != "agent/password-strength-password-strength-1782968998772" {
		t.Fatalf("source branch = %q", ref.SourceBranch)
	}
}

func TestParseGongfengMRRefsFromCommentDedupesURLs(t *testing.T) {
	content := "MR: https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/81\n" +
		"same again https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/81"

	refs := parseGongfengMRRefsFromComment(content)
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1: %+v", len(refs), refs)
	}
}

func TestSplitGongfengProjectPath(t *testing.T) {
	owner, repo := splitGongfengProjectPath("ChainWeaver/ida/user-center")
	if owner != "ChainWeaver/ida" || repo != "user-center" {
		t.Fatalf("split = %q, %q", owner, repo)
	}
}
