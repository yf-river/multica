import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('release-environments.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)


class DshRuntimeReleaseTest(unittest.TestCase):
    def test_build_uses_exact_source_and_checks_artifact_identity(self):
        for corrupt in ('none', 'hash', 'size', 'version', 'path'):
            with self.subTest(corrupt=corrupt), tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                source, target = root / 'source', root / 'target'
                package = source / 'packages/dsh-runtime'
                package.mkdir(parents=True)
                (package / 'package.json').write_text('{"version":"0.3.0"}')
                calls = []

                def command(args, **kwargs):
                    calls.append((args, kwargs))
                    if 'pack:runtime' not in args:
                        return
                    destination = Path(args[-1])
                    destination.mkdir(parents=True)
                    data = b'fixture packed bridge'
                    (destination / 'dsh-runtime-0.3.0.tgz').write_bytes(data)
                    manifest = dict(filename='dsh-runtime-0.3.0.tgz', bridgeVersion='0.3.0',
                                    sha256=hashlib.sha256(data).hexdigest(), bytes=len(data))
                    if corrupt == 'hash': manifest['sha256'] = '0' * 64
                    if corrupt == 'size': manifest['bytes'] += 1
                    if corrupt == 'version': manifest['bridgeVersion'] = '0.4.0'
                    if corrupt == 'path': manifest['filename'] = '../other.tgz'
                    (destination / 'manifest.json').write_text(json.dumps(manifest))

                with patch.object(r, 'run', side_effect=command):
                    if corrupt == 'none':
                        result = r.build_dsh_runtime(source, target, {'CONTROLLED': 'yes'})
                        self.assertEqual(result['bridgeVersion'], '0.3.0')
                    else:
                        with self.assertRaises(RuntimeError):
                            r.build_dsh_runtime(source, target, {})
                self.assertEqual(len(calls), 2)
                self.assertTrue(all(call[1]['cwd'] == source for call in calls))

    def test_old_source_without_bridge_has_no_new_commands(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(r, 'run') as command:
            self.assertIsNone(r.build_dsh_runtime(Path(folder), Path(folder) / 'target', {}))
            command.assert_not_called()


if __name__ == '__main__':
    unittest.main()
