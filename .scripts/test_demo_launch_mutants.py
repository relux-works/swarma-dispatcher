"""Bounded runner controls. No Go toolchain or subprocesses are used."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch, mock_open
from importlib.machinery import SourceFileLoader

spec = importlib.util.spec_from_file_location('mutants', Path(__file__).with_name('demo-launch-mutants.py'))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class RunnerTests(unittest.TestCase):
    def test_exact_and_combined_edits(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'a.go').write_text('first second')
            plant = dict(file='a.go', find='first', replace='one',
                         edits=[dict(file='a.go', find='second', replace='two')])
            runner.apply(root, plant)
            self.assertEqual((root / 'a.go').read_text(), 'one two')
            with self.assertRaises(ValueError):
                runner.apply(root, plant)
            (root / 'a.go').write_text('first first')
            with self.assertRaises(ValueError):
                runner.apply(root, plant)

    def test_failure_classification(self):
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'), expect='guard admitted forbidden input')
        self.assertEqual(runner.classify(1, 'guard_test.go:42: guard admitted forbidden input\n--- FAIL: TestGuard (0.1s)', False, plant), 'killed')
        for output in ['compile error', '--- SKIP: TestGuard', '--- FAIL: TestOther (0.1s)', 'panic: unrelated', '--- FAIL: TestGuard (0.1s)\npanic: runtime error: slice bounds out of range', 'guard_test.go:10: fixture setup failed\n--- FAIL: TestGuard', 'guard_test.go:42: wrong reason\n--- FAIL: TestGuard', 'guard_test.go:42: guard admitted forbidden input\n--- FAIL: TestGuard\nfatal error: runtime failure']:
            self.assertEqual(runner.classify(1, output, False, plant), 'invalid')
        self.assertEqual(runner.classify(0, '--- PASS: TestGuard (0.1s)', False, plant), 'survived')
        self.assertEqual(runner.classify(1, '--- FAIL: TestGuard', True, plant), 'invalid')
        plant.update(timeout_kills=True, timeout_reason='deliberate wait bound removal')
        self.assertEqual(runner.classify(1, '', True, plant), 'invalid')

    def test_build_failure_is_invalid(self):
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'), expect='guard admitted forbidden input')
        with tempfile.TemporaryDirectory() as tmp:
            tree = Path(tmp) / 'tree'
            tree.mkdir()
            log = Path(tmp) / 'log'
            with patch.object(runner, 'execute', return_value=(1, 'build failed', False)) as execute:
                self.assertEqual(runner.run_test(tree, plant, log, 1), 'invalid')
                self.assertEqual(execute.call_count, 1)
            self.assertIn('build failed', log.read_text())

    def test_no_matching_control_is_invalid(self):
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'), expect='guard admitted forbidden input')
        for text in ['PASS', '--- SKIP: TestGuard', '--- PASS: TestOther']:
            self.assertEqual(runner.classify(0, text, False, plant), 'invalid')

    def test_entry_timeout(self):
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'), expect='guard admitted forbidden input')
        with tempfile.TemporaryDirectory() as tmp:
            tree = Path(tmp) / 'tree'
            tree.mkdir()
            log = Path(tmp) / 'log'
            for override, expected in [(None, 90), (180, 180)]:
                if override is not None:
                    plant['timeout_seconds'] = override
                with patch.object(runner, 'execute', side_effect=[
                    (0, '', False), (1, 'guard_test.go:42: guard admitted forbidden input\n--- FAIL: TestGuard (100s)', False)
                ]) as execute:
                    self.assertEqual(runner.run_test(tree, plant, log, 90), 'killed')
                    args = execute.call_args_list[1].args
                    self.assertIn('-test.timeout=' + str(expected) + 's', args[0])
                    self.assertEqual(args[2], expected + 5)
            with patch.object(runner, 'execute', side_effect=[
                (0, '', False), (1, 'panic: test timed out after 3m', False)
            ]):
                self.assertEqual(runner.run_test(tree, plant, log, 90), 'invalid')
            for value in [0, -1, True, '180', 1.5]:
                plant['timeout_seconds'] = value
                with patch.object(runner, 'execute') as execute:
                    with self.assertRaises(ValueError):
                        runner.run_test(tree, plant, log, 90)
                    execute.assert_not_called()

    def test_fake_refusals(self):
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            fake, committed, log = root / 'fake', root / 'committed', root / 'log'
            committed.write_text('committed fake')
            fake.write_text('wrong')
            with self.assertRaisesRegex(ValueError, 'required'):
                runner.verify_fake(None, None, committed)
            with self.assertRaisesRegex(ValueError, 'bytes'):
                runner.verify_fake(fake, log, committed)
            fake.write_bytes(committed.read_bytes())
            real_stat = Path.stat
            def stat(path, *args, **kwargs):
                if str(path) == '/usr/bin/sudo':
                    value = real_stat(fake)
                    return SimpleNamespace(st_dev=value.st_dev, st_ino=value.st_ino + 1)
                return real_stat(path, *args, **kwargs)
            with patch.object(runner, 'SUDO_PATHS', ('/usr/bin/sudo',)), patch.object(runner.shutil, 'which', return_value=None), patch.object(Path, 'exists', return_value=True), patch.object(Path, 'stat', stat):
                with self.assertRaisesRegex(ValueError, 'not the fake'):
                    runner.verify_fake(fake, log, committed)
            def same_stat(path, *args, **kwargs):
                return real_stat(fake)
            with patch.object(runner, 'SUDO_PATHS', ('/usr/bin/sudo',)), patch.object(runner.shutil, 'which', return_value=None), patch.object(Path, 'exists', return_value=True), patch.object(Path, 'stat', same_stat), patch.object(runner, 'log_count', return_value=0), patch.object(runner.subprocess, 'run', return_value=SimpleNamespace(returncode=77)):
                with self.assertRaisesRegex(ValueError, 'self-test'):
                    runner.verify_fake(fake, log, committed)
            with patch.object(runner, 'SUDO_PATHS', ('/usr/bin/sudo',)), patch.object(runner.shutil, 'which', return_value=None), patch.object(Path, 'exists', return_value=True), patch.object(Path, 'stat', same_stat), patch.object(runner, 'log_count', side_effect=[0, 1]), patch.object(runner.subprocess, 'run', return_value=SimpleNamespace(returncode=77)):
                runner.verify_fake(fake, log, committed)

    def test_group_signal_precedes_reap(self):
        import io
        from types import SimpleNamespace
        events = []
        proc = SimpleNamespace(pid=123, stdout=io.StringIO('assertion output'), returncode=1)
        def wait():
            events.append('reap')
        proc.wait = wait
        def observe(*args):
            self.assertEqual(args, (runner.os.P_PID, 123, runner.os.WEXITED | runner.os.WNOWAIT | runner.os.WNOHANG))
            events.append('observe-unreaped')
            return object()
        with patch.object(runner.subprocess, 'Popen', return_value=proc), patch.object(runner.os, 'waitid', side_effect=observe), patch.object(runner.os, 'killpg', side_effect=lambda *args: events.append('signal-group')):
            self.assertEqual(runner.execute(['unused'], Path('.'), 1), (1, 'assertion output', False))
        self.assertEqual(events, ['observe-unreaped', 'signal-group', 'reap'])

    def test_fake_empty_child_environment(self):
        loader = SourceFileLoader('fake_sudo', str(Path(__file__).with_name('demo-fake-sudo')))
        fake_spec = importlib.util.spec_from_loader(loader.name, loader)
        fake = importlib.util.module_from_spec(fake_spec)
        loader.exec_module(fake)
        payload = b'UNRELATED=never-record-this\0DEMO_FAKE_SUDO_LOG=/fixture/log\0'
        with patch.dict(fake.os.environ, {}, clear=True), patch('builtins.open', mock_open(read_data=payload)) as opened:
            self.assertEqual(fake.log_path(), '/fixture/log')
            self.assertEqual(opened.call_args.args[0], '/proc/' + str(fake.os.getppid()) + '/environ')
        with patch.dict(fake.os.environ, {}, clear=True), patch('builtins.open', mock_open(read_data=b'UNRELATED=value\0')):
            with self.assertRaisesRegex(RuntimeError, 'unavailable'):
                fake.log_path()
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / 'calls'
            with patch.dict(fake.os.environ, {'DEMO_FAKE_SUDO_LOG': str(log)}), patch.object(fake.sys, 'argv', ['fake', '-n', '/bin/true']):
                self.assertEqual(fake.main(), 77)
            self.assertEqual(runner.log_count(log), 1)
            self.assertNotIn('UNRELATED', log.read_text())

    def test_disposable_required(self):
        with self.assertRaises(SystemExit) as caught:
            runner.main(['--logs', '/unused'])
        self.assertEqual(caught.exception.code, 2)


if __name__ == '__main__':
    unittest.main()
