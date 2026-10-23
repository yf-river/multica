import hashlib
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('seed', Path(__file__).with_name('verification-seed.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)

SHA = 'b' * 64


def write_seed(directory, manifest, payload=b'dump'):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / 'seed.dump').write_bytes(payload)
    manifest.setdefault('artifact', dict(dump='seed.dump', sha256=hashlib.sha256(payload).hexdigest()))
    (directory / 'seed.json').write_text(json.dumps(manifest))


class SeedManifestTests(unittest.TestCase):
    def test_read_seed_rejects_modified_dump(self):
        with tempfile.TemporaryDirectory() as tmp:
            seed = Path(tmp) / 'seed-1'
            write_seed(seed, dict(seed_id='seed-1', version=2))
            (seed / 'seed.dump').write_bytes(b'tampered')
            with self.assertRaisesRegex(RuntimeError, '种子文件校验失败'):
                s.read_seed(seed)

    def test_legacy_manifest_keeps_source_and_checksum(self):
        with tempfile.TemporaryDirectory() as tmp:
            seed = Path(tmp) / 'seed-20261022-final-v1'
            write_seed(seed, dict(source_environment='int-sanitized-from-verify-1', source_commit='86be17e7', collected_at='2026-10-22T00:00:00Z', database='multica_verify_1', dump_sha256=hashlib.sha256(b'dump').hexdigest()))
            path, manifest, artifact = s.read_seed(seed)
            self.assertEqual(artifact['sha256'], hashlib.sha256(b'dump').hexdigest())
            source = s.source_identity(manifest)
            self.assertEqual(source['environment'], 'int-sanitized-from-verify-1')
            self.assertEqual(source['commit'], '86be17e7')
            self.assertEqual(source['database'], 'multica_verify_1')

    def test_scrub_identity_tracks_rules_file(self):
        identity = s.scrub_identity()
        self.assertEqual(identity['version'], 2)
        self.assertEqual(len(identity['sha256']), 64)


class EnsureTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        patcher = patch.object(s.r, 'RUN', Path(self.tmp.name))
        patcher.start()
        self.addCleanup(patcher.stop)

    def seed(self):
        self.addCleanup(patch.stopall)
        patcher = patch.object(s, 'SEEDS', Path(self.tmp.name) / 'seeds')
        patcher.start()
        seed = Path(self.tmp.name) / 'seeds' / 'seed-current'
        write_seed(seed, dict(seed_id='seed-current', version=2, restore_verification=dict(passed=True)))
        s.r.write(s.current_path(), dict(seed_id='seed-current', dump_sha256=hashlib.sha256(b'dump').hexdigest()))
        return seed

    def test_without_current_pointer_keeps_existing_behaviour(self):
        with patch.object(s, 'slot_seed') as slot, patch.object(s, 'reset') as reset:
            self.assertEqual(s.ensure('verify-1', Path('/pg'))['status'], 'skipped')
            slot.assert_not_called()
            reset.assert_not_called()

    def test_matching_verified_slot_is_reused(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=dict(seed_id='seed-current', seed_sha256=hashlib.sha256(b'dump').hexdigest(), seed_verified=True)), patch.object(s, 'reset') as reset:
            self.assertEqual(s.ensure('verify-1', Path('/pg'))['status'], 'current')
            reset.assert_not_called()

    def test_stale_or_unverified_slot_is_restored_before_claim(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=dict(seed_id='old', seed_sha256=SHA, seed_verified=True)), patch.object(s, 'reset') as reset:
            self.assertEqual(s.ensure('verify-1', Path('/pg'))['status'], 'restored')
            reset.assert_called_once()

    def test_unverified_same_seed_is_restored_again(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=dict(seed_id='seed-current', seed_sha256=hashlib.sha256(b'dump').hexdigest(), seed_verified=False)), patch.object(s, 'reset') as reset:
            s.ensure('verify-1', Path('/pg'))
            reset.assert_called_once()

    def test_missing_slot_record_is_restored(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=None), patch.object(s, 'reset') as reset:
            s.ensure('verify-1', Path('/pg'))
            reset.assert_called_once()


class ResetTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        patcher = patch.object(s.r, 'RUN', Path(self.tmp.name))
        patcher.start()
        self.addCleanup(patcher.stop)
        self.seed = Path(self.tmp.name) / 'seed'
        write_seed(self.seed, dict(seed_id='seed-x', version=2, restore_verification=dict(passed=True)))

    def test_success_marks_seed_and_resumes_slot(self):
        with patch.object(s, 'maintain') as maintain, patch.object(s, 'restore', return_value=dict(seed_id='seed-x')), patch.object(s, 'record_slot_seed') as record, patch.object(s, 'resume') as resume:
            s.reset('verify-1', self.seed, Path('/pg'))
            maintain.assert_called_once()
            record.assert_called_once()
            self.assertTrue(record.call_args[0][3])
            resume.assert_called_once()

    def test_failure_keeps_slot_in_maintenance_and_marks_unverified(self):
        with patch.object(s, 'maintain'), patch.object(s, 'restore', side_effect=RuntimeError('恢复失败')), patch.object(s, 'record_slot_seed') as record, patch.object(s, 'resume') as resume:
            with self.assertRaisesRegex(RuntimeError, '槽位保持维护'):
                s.reset('verify-1', self.seed, Path('/pg'))
            self.assertFalse(record.call_args[0][3])
            resume.assert_not_called()


if __name__ == '__main__':
    unittest.main()
