#!/usr/bin/env python3
"""构建前预检：只跑声明为 prebuild 的秒级检查，失败时不进入构建。"""
import importlib.util
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent


def load_acceptance(root):
    os.environ['MULTICA_RELEASE_ROOT'] = str(root)
    spec = importlib.util.spec_from_file_location('acceptance_under_test', SCRIPTS / 'run-delivery-acceptance.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PrebuildTests(unittest.TestCase):
    def recipe(self, root, groups):
        source = root / 'source'
        (source / 'scripts').mkdir(parents=True)
        (source / 'scripts' / 'delivery-acceptance.json').write_text(json.dumps({'groups': groups}))
        return source

    def test_runs_only_prebuild_groups_and_reports_them(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            acceptance = load_acceptance(root)
            source = self.recipe(root, [
                {'name': 'fast', 'command': [sys.executable, '-c', 'print(1)'], 'prebuild': True},
                {'name': 'slow', 'command': [sys.executable, '-c', 'raise SystemExit(9)']},
            ])
            checks = acceptance.execute_prebuild(source)
            self.assertEqual([c['name'] for c in checks], ['fast'])
            self.assertEqual(checks[0]['status'], 'passed')
            log = root / '.run' / 'delivery-evidence' / 'prebuild-fast.log'
            self.assertTrue(log.exists())

    def test_failing_prebuild_stops_before_build(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            acceptance = load_acceptance(root)
            source = self.recipe(root, [
                {'name': 'broken', 'command': [sys.executable, '-c', 'raise SystemExit(3)'], 'prebuild': True},
            ])
            with self.assertRaisesRegex(RuntimeError, '预检失败：broken'):
                acceptance.execute_prebuild(source)

    def test_missing_recipe_is_a_noop(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            acceptance = load_acceptance(root)
            source = root / 'bare'
            source.mkdir()
            self.assertEqual(acceptance.execute_prebuild(source), [])


if __name__ == '__main__':
    unittest.main()
