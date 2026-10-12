import importlib.util
from pathlib import Path
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

class ReleaseTests(unittest.TestCase):
    def test_snapshot_relocates_absolute_workspace_links(self):
        with tempfile.TemporaryDirectory() as d:
            source=Path(d)/'source';stage=Path(d)/'stage'
            for root in (source,stage):
                (root/'packages/core').mkdir(parents=True)
            modules=stage/'packages/views/node_modules/@multica';modules.mkdir(parents=True)
            link=modules/'core';link.symlink_to(source/'packages/core')
            r.relocate_dependency_links(stage,source)
            self.assertEqual(link.resolve(),stage/'packages/core')

    def test_existing_configuration_is_not_overwritten(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d)
            try:
                config=r.config_path('int');r.write(config,{'secret':'original'},True)
                before=config.read_bytes()
                with self.assertRaisesRegex(RuntimeError,'已初始化'): r.initialize('int')
                self.assertEqual(config.read_bytes(),before)
                self.assertEqual(config.stat().st_mode&0o777,0o600)
            finally:r.RUN=original

    def test_checksum_detects_changed_files_and_symlink_targets(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'app').write_text('first');(root/'link').symlink_to('app')
            before=r.checksums(root);(root/'app').write_text('second')
            self.assertNotEqual(before,r.checksums(root))
            self.assertEqual(before['link'],'link:app')

    def test_package_cannot_depend_on_external_mutable_directories(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)/'release';root.mkdir()
            outside=Path(d)/'development';outside.mkdir()
            (root/'node_modules').symlink_to(outside,target_is_directory=True)
            with self.assertRaisesRegex(RuntimeError,'符号链接'):r.checksums(root)

    def test_same_environment_deployments_are_serialized(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d)
            try:
                with r.lock('int'):
                    with self.assertRaises(BlockingIOError):
                        with r.lock('int'):pass
                    with r.lock('prod'):pass
            finally:r.RUN=original

if __name__=='__main__':unittest.main()
