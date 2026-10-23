#!/usr/bin/env python3
"""同步托管质量角色的工作区技能和默认提示词；默认只预览。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
ROLES = {"review": "review/settings", "supervision": "supervision"}
MARKER = "quality_agent_source"


def sha(text):
    return hashlib.sha256(text.encode()).hexdigest()


class API:
    def __init__(self, profile):
        self.profile = profile
        self.base = profile["server_url"].rstrip("/")
        url = urllib.parse.urlsplit(self.base)
        if url.scheme not in ("http", "https") or not url.hostname or url.username or url.password:
            raise ValueError("无效服务地址")
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def __call__(self, method, route, body=None):
        request = urllib.request.Request(self.base + "/api/" + route, method=method,
            data=None if body is None else json.dumps(body).encode(), headers={
                "Authorization": "Bearer " + self.profile["token"],
                "X-Workspace-ID": self.profile["workspace_id"], "Content-Type": "application/json"})
        try:
            with self.opener.open(request, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"API HTTP {error.code}；未自动重试") from None
        except (urllib.error.URLError, TimeoutError, OSError):
            raise RuntimeError("API 连接中断；写入结果需按备份对账，未自动重试") from None


def settings(role, response):
    config = response["config"] if role == "supervision" else response
    keys = ["agent_id", "enabled", "runtime_id", "model", "thinking_level", "instructions"]
    if role == "supervision":
        keys += ["interval_seconds", "investigation_enabled", "concurrency"]
    return {key: config.get(key) for key in keys}


def bundle(role, root):
    name = "delivery-" + role
    content = (root / "skills" / name / "SKILL.md").read_text()
    description = next(line.removeprefix("description: ") for line in content.splitlines()
                       if line.startswith("description: "))
    instructions = (root / "server/internal" / role / "default_instructions.md").read_text().strip()
    return {"name": name, "description": description, "content": content}, instructions


def prepare(api, root=ROOT):
    all_skills = api("GET", "skills")
    plan = []
    for role, route in ROLES.items():
        response = api("GET", route)
        before = settings(role, response)
        if not response.get("can_manage") or not before["agent_id"]:
            raise ValueError(f"{role} 未配置或没有管理权限")
        desired, instructions = bundle(role, root)
        matches = [skill for skill in all_skills if skill["name"] == desired["name"]]
        if len(matches) > 1:
            raise ValueError("技能重名，需先核对归属")
        existing = api("GET", "skills/" + matches[0]["id"]) if matches else None
        config = (existing or {}).get("config") or {}
        if not isinstance(config, dict):
            raise ValueError("技能配置不是对象，保留原数据")
        marker = config.get(MARKER, {})
        if existing and (marker.get("path") != f"skills/{desired['name']}/SKILL.md"
                         or marker.get("sha256") != sha(existing["content"])):
            raise ValueError("同名技能不是本脚本维护的版本，保留用户修改")
        custom = (before.get("instructions") or "").strip()
        known = {"", response.get("default_instructions", "").strip(), instructions}
        replace = custom in known or sha(custom) == marker.get("instructions_sha256")
        after = {k: v for k, v in before.items() if k not in ("agent_id", "concurrency")}
        target = "" if instructions == response.get("default_instructions", "").strip() else instructions
        after["instructions"] = target if replace else before["instructions"]
        desired["config"] = {**config, MARKER: {
            "path": f"skills/{desired['name']}/SKILL.md", "sha256": sha(desired["content"]),
            "instructions_sha256": sha(instructions)}}
        bindings = api("GET", f"agents/{before['agent_id']}/skills")
        binding = next((s for s in bindings if existing and s["id"] == existing["id"]), None)
        if binding and not binding.get("enabled"):
            raise ValueError("该技能已被用户禁用，保留禁用决定")
        plan.append(dict(role=role, route=route, before=before, after=after, existing=existing,
                         desired=desired, bindings=bindings, bind=binding is None,
                         custom_preserved=not replace))
    return plan


def apply(api, plan):
    # 先核对两类设置及技能，避免用旧快照覆盖用户刚保存的内容。
    for item in plan:
        if settings(item["role"], api("GET", item["route"])) != item["before"]:
            raise ValueError("设置已变化，请重新预览")
        if item["existing"] and api("GET", "skills/" + item["existing"]["id"]) != item["existing"]:
            raise ValueError("技能已变化，请重新预览")
        if api("GET", f"agents/{item['before']['agent_id']}/skills") != item["bindings"]:
            raise ValueError("技能绑定已变化，请重新预览")
    for item in plan:
        existing, desired = item["existing"], item["desired"]
        if existing:
            skill_id = existing["id"]
            if any(existing.get(k) != v for k, v in desired.items()):
                api("PUT", "skills/" + skill_id, desired)
        else:
            skill_id = api("POST", "skills", desired)["id"]
        if item["bind"]:
            api("POST", f"agents/{item['before']['agent_id']}/skills/add", {"skill_ids": [skill_id]})
        current = settings(item["role"], api("GET", item["route"]))
        if current != item["before"]:
            raise ValueError("设置在技能同步期间变化，已保留；请重新预览")
        if any(current.get(k) != v for k, v in item["after"].items()):
            api("PUT", item["route"], item["after"])
        actual = settings(item["role"], api("GET", item["route"]))
        if any(actual.get(k) != v for k, v in item["after"].items()):
            raise RuntimeError("设置回读不一致，请按备份核对")
        bound = api("GET", f"agents/{item['before']['agent_id']}/skills")
        if not any(s["id"] == skill_id and s.get("enabled") for s in bound):
            raise RuntimeError("技能绑定回读不一致")
        actual_skill = api("GET", "skills/" + skill_id)
        if any(actual_skill.get(k) != v for k, v in desired.items()):
            raise RuntimeError("技能内容回读不一致")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", type=Path, required=True)
    parser.add_argument("--environment", required=True)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--backup", type=Path)
    args = parser.parse_args()
    profile = json.loads(args.profile.read_text())
    if profile["workspace_id"] != args.workspace:
        parser.error("profile 工作区与目标不一致")
    api = API(profile)
    if api("GET", "config").get("environment_id") != args.environment:
        parser.error("服务端环境身份与目标不一致")
    user = api("GET", "me")
    workspace = api("GET", "workspaces/" + args.workspace)
    if workspace["id"] != args.workspace:
        parser.error("工作区不可访问")
    plan = prepare(api)
    print(json.dumps({"environment": args.environment, "workspace": workspace["id"],
        "user": user["id"], "roles": [{"role": p["role"], "custom_preserved": p["custom_preserved"],
        "create_skill": p["existing"] is None, "bind": p["bind"]} for p in plan]}, ensure_ascii=False))
    if args.apply:
        if not args.backup:
            parser.error("--apply 需要 --backup 保存变更前快照")
        with os.fdopen(os.open(args.backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as out:
            json.dump({"workspace": args.workspace, "environment": args.environment, "plan": plan}, out,
                      ensure_ascii=False, indent=2)
        apply(api, plan)
        print("设置及技能已同步并回读；运行中的任务未取消或重启。")


if __name__ == "__main__":
    main()
