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

func TestParseGongfengMRRefsFromCommentKeepsPerMRBranch(t *testing.T) {
	content := "## 04-开发 CodeReview 返工 handoff\n\n" +
		"### AIS-119 — gateway !267\n\n" +
		"| 项目 | 内容 |\n" +
		"|------|------|\n" +
		"| MR | https://git.code.tencent.com/ChainWeaver/ida/gateway/merge_requests/267 |\n" +
		"| Source branch | agent/issue/0470e184 (已推送) |\n\n" +
		"### AIS-120 — ida-deployment !209\n\n" +
		"| 项目 | 内容 |\n" +
		"|------|------|\n" +
		"| MR | https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/merge_requests/209 |\n" +
		"| Source branch | agent/issue/c900e5ac (已推送) |\n"

	refs := parseGongfengMRRefsFromComment(content)
	if len(refs) != 2 {
		t.Fatalf("refs len = %d, want 2: %+v", len(refs), refs)
	}
	if refs[0].ProjectPath != "ChainWeaver/ida/gateway" || refs[0].Number != 267 || refs[0].SourceBranch != "agent/issue/0470e184" {
		t.Fatalf("gateway ref = %+v", refs[0])
	}
	if refs[0].Title != "AIS-119 — gateway !267" {
		t.Fatalf("gateway title = %q", refs[0].Title)
	}
	if refs[1].ProjectPath != "ChainWeaver/ida/ida-deployment" || refs[1].Number != 209 || refs[1].SourceBranch != "agent/issue/c900e5ac" {
		t.Fatalf("ida-deployment ref = %+v", refs[1])
	}
	if refs[1].Title != "AIS-120 — ida-deployment !209" {
		t.Fatalf("ida-deployment title = %q", refs[1].Title)
	}
}

func TestParseGongfengMRRefsFromCommentDoesNotInferTaskBranch(t *testing.T) {
	content := "## 05-verify 验证结论\n\n" +
		"### gateway MR [#273](https://git.code.tencent.com/ChainWeaver/ida/gateway/merge_requests/273)\n\n" +
		"| 验证项 | 结果 |\n" +
		"|--------|------|\n" +
		"| Commit `2bcf4b30` 已推送 | ✅ |\n\n" +
		"当前验证任务运行在 agent/3f1fda59-0b74-4362-a6f5-4acb991c83cb worktree。\n\n" +
		"### ida-deployment MR [#215](https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/merge_requests/215)\n\n" +
		"| Commit `21777525` 已推送 | ✅ |\n"

	refs := parseGongfengMRRefsFromComment(content)
	if len(refs) != 2 {
		t.Fatalf("refs len = %d, want 2: %+v", len(refs), refs)
	}
	for _, ref := range refs {
		if ref.SourceBranch != "" {
			t.Fatalf("ref %s source branch = %q, want empty without explicit source branch label", ref.HTMLURL, ref.SourceBranch)
		}
	}
}

func TestParseGongfengMRRefsFromCommentSupportsDashMergeRequestURL(t *testing.T) {
	content := "MR: https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/-/merge_requests/216\n" +
		"Source branch | agent/issue/4b46b5a9\n"

	refs := parseGongfengMRRefsFromComment(content)
	if len(refs) != 1 {
		t.Fatalf("refs len = %d, want 1: %+v", len(refs), refs)
	}
	ref := refs[0]
	if ref.ProjectPath != "ChainWeaver/ida/ida-deployment" {
		t.Fatalf("project path = %q", ref.ProjectPath)
	}
	if ref.Number != 216 {
		t.Fatalf("number = %d", ref.Number)
	}
	if ref.SourceBranch != "agent/issue/4b46b5a9" {
		t.Fatalf("source branch = %q", ref.SourceBranch)
	}
}

func TestSplitGongfengProjectPath(t *testing.T) {
	owner, repo := splitGongfengProjectPath("ChainWeaver/ida/user-center")
	if owner != "ChainWeaver/ida" || repo != "user-center" {
		t.Fatalf("split = %q, %q", owner, repo)
	}
}
