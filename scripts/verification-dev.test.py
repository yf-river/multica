import importlib.util
import tempfile
import unittest
from types import SimpleNamespace
from pathlib import Path
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('verification_dev',Path(__file__).with_name('verification-dev.py'))
dev=importlib.util.module_from_spec(spec);spec.loader.exec_module(dev)


class GeneratedFileTests(unittest.TestCase):
    def test_only_restores_next_generated_content(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/'next-env.d.ts'
            original=b'import "./.next/types/routes.d.ts";\n'
            path.write_bytes(original.replace(b'./.next/types/',b'./.next/dev/types/'))
            dev.update_next_env(path,original)
            self.assertEqual(path.read_bytes(),original)
            path.write_bytes(b'user changes')
            with self.assertRaises(RuntimeError): dev.update_next_env(path,original)
            self.assertEqual(path.read_bytes(),b'user changes')

    def test_bridge_build_output_passes_inventory_but_unknown_files_do_not(self):
        with tempfile.TemporaryDirectory() as directory:
            source=Path(directory)
            release=SimpleNamespace(RUN=source,VERIFICATION_ENVS=('verify-1',),read=lambda p:{},config_path=lambda e:source/'config.json',meta_path=lambda e:source/'meta.json',validate_verification_config=lambda *a:None,check_process_ownership=lambda *a:None,check_live_environment=lambda *a:None)
            lease=dict(name='verify-1',delivery_id='delivery',generation=1)
            for ignored in ['packages/dsh-runtime/dist/','node_modules/\npackages/dsh-runtime/dist/']:
                release.output=lambda *a,**k:ignored
                # 目录检查通过后仍执行下一项前端配置保护。
                with self.assertRaisesRegex(RuntimeError,'前端未关闭'):
                    dev.start(release,'verify-1',source,lease)
            for ignored in ['private-cache/','packages/dsh-runtime/secrets/','packages/other/dist/']:
                release.output=lambda *a,**k:ignored
                with self.assertRaisesRegex(RuntimeError,'未识别的忽略文件'):
                    dev.start(release,'verify-1',source,lease)

    def test_acl_restore_ignores_cache_files_removed_by_next(self):
        with tempfile.TemporaryDirectory() as directory:
            source=Path(directory)
            for name in ('apps/web/.next','apps/web/.source'):(source/name).mkdir(parents=True)
            nextenv=source/'apps/web/next-env.d.ts';nextenv.write_text('x')
            existing=source/'apps/web/.next/current';existing.write_text('x')
            removed=source/'apps/web/.next/removed'
            payload=(f'# file: {existing}\nuser::rw-\ngroup::r--\nother::r--\n\n# file: {removed}\nuser::rw-\ngroup::r--\nother::r--\n').encode()
            with patch.object(dev.subprocess,'run') as run:
                dev.restore_acl(payload,source)
            self.assertIn(str(existing).encode(),run.call_args.kwargs['input'])
            self.assertNotIn(str(removed).encode(),run.call_args.kwargs['input'])


if __name__=='__main__':unittest.main()
