import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("quality_skills", Path(__file__).with_name("quality-agent-skills.py"))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class Workspace:
    """内存 API，用于检查写入内容、失败边界及重复同步。"""
    def __init__(self):
        self.configs = {role: dict(agent_id=role, enabled=False, runtime_id="runtime",
            model="chosen-model", thinking_level="high", instructions="") for role in sync.ROLES}
        self.configs["supervision"].update(interval_seconds=600, investigation_enabled=False, concurrency=3)
        self.skills = {}
        self.bindings = {role: [dict(id="user-skill", enabled=True)] for role in sync.ROLES}
        self.writes = []

    def __call__(self, method, route, body=None):
        result = self.request(method, route, body)
        return copy.deepcopy(result)

    def request(self, method, route, body):
        if method != "GET":
            self.writes.append((method, route, copy.deepcopy(body)))
        if route in sync.ROLES.values():
            role = next(r for r, path in sync.ROLES.items() if path == route)
            if method == "PUT":
                self.configs[role].update(body)
            value = self.configs[role]
            return dict(can_manage=True, config=value) if role == "supervision" else dict(
                value, can_manage=True, default_instructions="old default")
        if route == "skills":
            if method == "POST":
                value = dict(body, id=str(len(self.skills) + 1))
                self.skills[value["id"]] = value
                return value
            return list(self.skills.values())
        if route.startswith("skills/"):
            value = self.skills[route.split("/")[1]]
            if method == "PUT":
                value.update(body)
            return value
        if route.startswith("agents/"):
            role = route.split("/")[1]
            if method == "POST":
                self.bindings[role] += [dict(id=id, enabled=True) for id in body["skill_ids"]]
            return self.bindings[role]
        raise AssertionError((method, route))


class SyncTest(unittest.TestCase):
    def test_deployed_default_is_saved_as_empty_to_follow_future_updates(self):
        api = Workspace()
        original = api.request
        def updated_default(method, route, body):
            value = original(method, route, body)
            if route == "review/settings":
                value = dict(value, default_instructions=sync.bundle("review", sync.ROOT)[1])
            return value
        api.request = updated_default
        sync.apply(api, sync.prepare(api))
        self.assertEqual(api.configs["review"]["instructions"], "")

    def test_preserves_configuration_and_isolates_role_bindings(self):
        api = Workspace()
        before = copy.deepcopy(api.configs)
        api.configs["review"]["instructions"] = "old default"
        sync.apply(api, sync.prepare(api))
        for role in sync.ROLES:
            for key, value in before[role].items():
                if key != "instructions":
                    self.assertEqual(api.configs[role][key], value)
            bound = api.bindings[role]
            self.assertEqual(bound[0]["id"], "user-skill")
            self.assertEqual(len(bound), 2)
            self.assertEqual(api.skills[bound[1]["id"]]["name"], "delivery-" + role)
        self.assertNotEqual(api.bindings["review"][1]["id"], api.bindings["supervision"][1]["id"])

    def test_second_apply_has_no_writes_and_preserves_extra_files(self):
        api = Workspace()
        sync.apply(api, sync.prepare(api))
        skill = next(iter(api.skills.values()))
        skill["files"] = [{"path": "user-note.txt", "content": "keep"}]
        api.writes.clear()
        sync.apply(api, sync.prepare(api))
        self.assertEqual(api.writes, [])
        self.assertEqual(skill["files"][0]["content"], "keep")

    def test_unknown_custom_instructions_are_not_replaced(self):
        api = Workspace()
        api.configs["review"]["instructions"] = "用户专门要求"
        plan = sync.prepare(api)
        self.assertTrue(plan[0]["custom_preserved"])
        sync.apply(api, plan)
        self.assertEqual(api.configs["review"]["instructions"], "用户专门要求")

    def test_user_changed_skill_and_disabled_binding_stop_before_writes(self):
        for change in ("content", "enabled"):
            api = Workspace()
            sync.apply(api, sync.prepare(api))
            if change == "content":
                api.skills["1"]["content"] = "用户维护的版本"
            else:
                api.bindings["review"][1]["enabled"] = False
            api.writes.clear()
            with self.assertRaises(ValueError):
                sync.prepare(api)
            self.assertEqual(api.writes, [])

    def test_concurrent_setting_change_is_not_overwritten(self):
        api = Workspace()
        plan = sync.prepare(api)
        api.configs["supervision"]["enabled"] = True
        with self.assertRaises(ValueError):
            sync.apply(api, plan)
        self.assertEqual(api.writes, [])

    def test_resume_after_partial_apply_reuses_created_skill(self):
        api = Workspace()
        original = api.request
        def fail(method, route, body):
            if method == "POST" and route == "agents/review/skills/add":
                raise RuntimeError("connection interrupted")
            return original(method, route, body)
        api.request = fail
        with self.assertRaises(RuntimeError):
            sync.apply(api, sync.prepare(api))
        self.assertEqual(len(api.skills), 1)
        api.request = original
        sync.apply(api, sync.prepare(api))
        self.assertEqual(len(api.skills), 2)
        self.assertEqual(len(api.bindings["review"]), 2)


if __name__ == "__main__":
    unittest.main()
