"""检查清理钩子对实际 shell 退出结果的影响，不启动任何服务。"""
import pathlib
import subprocess
import unittest

class CheckExitTests(unittest.TestCase):
    def test_exit_status(self):
        source = pathlib.Path(__file__).with_name('check.sh').read_text()
        cleanup = source[source.index('cleanup() {'):source.index('trap cleanup EXIT')]
        setup = 'set -euo pipefail\nSTARTED_BACKEND=false\nSTARTED_FRONTEND=false\nBACKEND_PID=""\nFRONTEND_PID=""\nEXIT_CODE=0\n'
        for command, code in [('false', 1), ('exit 23', 23), ('true', 0), ('EXIT_CODE=7; exit 0', 7)]:
            with self.subTest(command=command):
                result = subprocess.run(['bash', '-c', setup + cleanup + '\ntrap cleanup EXIT\n' + command], capture_output=True, text=True)
                self.assertEqual(result.returncode, code, result.stdout + result.stderr)
                self.assertEqual('All checks passed' in result.stdout, code == 0)

if __name__ == '__main__':
    unittest.main()
