#!/usr/bin/env python3
"""在独立、无凭据的 profile 中运行真实 DSH 和确定性模型夹具。"""
import argparse
import hashlib
import struct
import zlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def main():
    if os.environ.get("MULTICA_RUN_REAL_AGENT_SMOKE") != "1":
        raise SystemExit("Set MULTICA_RUN_REAL_AGENT_SMOKE=1 to run the explicit native smoke")
    parser = argparse.ArgumentParser()
    parser.add_argument("--dsh", type=Path, required=True)
    parser.add_argument("--host-package", type=Path, required=True)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path, required=True)
    args = parser.parse_args()
    args.artifacts = args.artifacts.resolve()
    args.artifacts.mkdir(parents=True, exist_ok=True, mode=0o700)
    root = Path(tempfile.mkdtemp(prefix="native-interactions-", dir=args.artifacts))
    home = root / "home"
    home.mkdir(mode=0o700)
    requests_file = root / "requests.jsonl"
    fixture = Path(__file__).resolve().parent.parent / "tests/fixtures/native-provider.mjs"
    env = dict(os.environ, DSH_HOME=str(home), MULTICA_DSH_INTERACTIONS="1",
               MULTICA_DSH_HOST_LLM_MODULE=(args.host_package / "node_modules/@deepseek-ai/dsh-llm/lib/index.js").resolve().as_uri(),
               MULTICA_DSH_FIXTURE_REQUESTS=str(requests_file))
    for key in list(env):
        if any(part in key for part in ("API_KEY", "AUTH_TOKEN", "MULTICA_TOKEN")):
            env.pop(key)
    installed = subprocess.run([str(args.dsh), "plugin", "--profile", "multica", "add", str(args.bundle.resolve())],
                               env=env, capture_output=True, text=True, timeout=90)
    (root / "install.log").write_text(installed.stdout + installed.stderr)
    if installed.returncode:
        raise RuntimeError(f"fixture profile installation failed; see {root / 'install.log'}")
    profile = home / "profiles/multica"
    (profile / "cordis.patch.yml").write_text(
        "- insert:\n    - id: native-interaction-fixture\n      name: " + fixture.as_uri() +
        "\n- id: agent-default-model\n  config:\n    provider: multica-fixture\n    model: native-interaction\n")
    reports = []
    enabled_since = int(time.time()*1000)

    def calls():
        return [json.loads(line) for line in requests_file.read_text().splitlines()] if requests_file.exists() else []

    def run(phase, folder, prompt, command=None, session=None, decision=None):
        folder.mkdir(exist_ok=True, mode=0o700)
        request = {"v": 1, "type": "execute", "request_id": "fixture-" + phase, "cwd": str(folder),
                   "prompt": prompt, "human_interactions": True, "insights_since": enabled_since,
                   "model": {"provider": "multica-fixture", "id": "native-interaction"}, "mcp_servers": []}
        if command:
            request["command"] = command
        if session:
            request["resume_session_id"] = session
        if decision:
            request["plan_decision"] = decision
        start = len(calls())
        process = subprocess.run([str(args.dsh), "--profile", "multica", "--stdio"],
                                 input=json.dumps(request) + "\n", text=True, capture_output=True,
                                 env=dict(env, MULTICA_DSH_FIXTURE_PHASE=phase), timeout=45)
        (root / (phase + ".frames.jsonl")).write_text(process.stdout)
        (root / (phase + ".stderr.log")).write_text(process.stderr)
        frames = [json.loads(line) for line in process.stdout.splitlines() if line.startswith("{")]
        result = next((frame for frame in reversed(frames) if frame.get("type") == "result"), None)
        if result is None:
            raise AssertionError(f"{phase} has no result; see {root}")
        current_calls = calls()[start:]
        if current_calls:
            insights = [frame for frame in frames if frame.get("type")=="insights"]
            assert insights and any("contextTimeline" in f["projections"] for f in insights), f"missing insight projections: {phase}"
            assert any(frame.get("type")=="archive" for frame in frames), f"missing original events: {phase}"
        # 不允许标题生成等隐式调用藏在主任务计量之外。
        assert all(call.get("tools") for call in current_calls), f"unexpected auxiliary model call in {phase}"
        reports.append({"phase": phase, "exit": process.returncode, "status": result["status"],
                        "session_id": result.get("session_id"), "model_calls": len(current_calls)})
        return result, frames, current_calls

    work = root / "work"
    plan, frames, captured = run("plan", work, "Plan a write to approved.txt. Wait for approval.", "/plan")
    assert plan["status"] == "waiting_plan" and len(captured) == 1
    assert len([frame for frame in frames if frame["type"] == "plan_review"]) == 1
    assert any(frame["type"] == "tool_result" for frame in frames)
    assert not (work / "approved.txt").exists()
    keep, _, captured = run("keep-planning", work, "继续规划，暂不执行。补充回滚步骤。", "/plan", plan["session_id"], "keep_planning")
    assert keep["status"] == "waiting_plan" and keep["session_id"] == plan["session_id"] and len(captured) == 1
    assert not (work / "approved.txt").exists()
    approved, _, captured = run("approved", work, "已批准本轮计划，请按计划执行。", "/plan off", keep["session_id"], "approved")
    assert approved["status"] == "completed" and len(captured) == 2
    assert (work / "approved.txt").read_text() == "DSH_APPROVED_WRITE"
    assert approved["session_id"] == plan["session_id"]
    assert any(message["role"] == "tool" for message in captured[0]["messages"])
    question_work = root / "question-work"
    question, frames, captured = run("question", question_work, "Ask which folder to use.")
    assert question["status"] == "waiting_input" and len(captured) == 1
    assert len([frame for frame in frames if frame["type"] == "user_question"]) == 1
    answer, _, captured = run("answer", question_work, "对上一轮问题的回答：\ndestination: docs", session=question["session_id"])
    assert answer["status"] == "completed" and answer["session_id"] == question["session_id"] and len(captured) == 1
    assert "destination: docs" in json.dumps(captured[0]["messages"], ensure_ascii=False)
    image_work = root / "image-work"
    image_work.mkdir()
    def png_chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))
    image_bytes = b"\x89PNG\r\n\x1a\n" + png_chunk(b"IHDR", struct.pack(">IIBBBBB", 16, 16, 8, 2, 0, 0, 0)) + png_chunk(b"IDAT", zlib.compress((b"\x00" + b"\xff\x00\x00" * 16) * 16)) + png_chunk(b"IEND", b"")
    (image_work / "image.png").write_bytes(image_bytes)
    image, image_frames, captured = run("image", image_work, "Read image.png with the native image tool.")
    assert image["status"] == "completed" and len(captured) == 2
    attachment_id = "sha256:" + hashlib.sha256(image_bytes).hexdigest()
    assert attachment_id in json.dumps(image_frames), "native image attachment reference was lost"
    original = home / "attachments/v1/objects" / attachment_id[7:9] / attachment_id[7:]
    assert original.read_bytes() == image_bytes, "native image original changed"
    large, large_frames, captured = run("large-answer", work, "Return the complete large fixture answer.")
    assert large["status"] == "completed" and len(captured) == 1
    original_text = "INSIGHTS-LARGE-START\n" + "完整原文" * 30000 + "\nINSIGHTS-LARGE-END"
    assert any(frame.get("event", {}).get("type") == "assistant/message" and original_text in json.dumps(frame, ensure_ascii=False).replace("\\n", "\n") for frame in large_frames), "large native context body was truncated"
    failed, _, captured = run("provider-error", work, "Execute the approved work.", "/plan off", approved["session_id"], "approved")
    assert failed["status"] == "failed" and len(captured) == 1
    assert "Deterministic fixture provider failure" in failed["error"]["message"]
    rejected, _, captured = run("missing-session", work, "已批准本轮计划，请按计划执行。", "/plan off", "multica-fixture-missing-session", "approved")
    assert rejected["status"] == "failed" and not captured
    host_metadata = json.loads((args.host_package / "package.json").read_text())
    result = {"status": "PASS", "dsh_version": host_metadata["version"], "model": "local deterministic fixture",
              "paid_model_calls": 0, "checks": reports}
    (root / "report.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"status": "PASS", "report": str(root / "report.json"), "checks": reports}, ensure_ascii=False))


if __name__ == "__main__":
    main()
