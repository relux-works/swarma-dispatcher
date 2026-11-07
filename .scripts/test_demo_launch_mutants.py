"""Bounded runner controls; Python subprocess fixtures only, no Go or sudo."""
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
        self.assertEqual(runner.classify(1, '=== RUN   TestGuard\n    guard_test.go:42: guard admitted forbidden input\n--- FAIL: TestGuard (0.1s)', False, plant), 'killed')
        for output in ['compile error', '--- SKIP: TestGuard', '--- FAIL: TestOther (0.1s)', 'panic: unrelated', '--- FAIL: TestGuard (0.1s)\npanic: runtime error: slice bounds out of range', 'guard_test.go:10: fixture setup failed\n--- FAIL: TestGuard', 'guard_test.go:42: wrong reason\n--- FAIL: TestGuard', '=== RUN   TestGuard\n    guard_test.go:42: guard admitted forbidden input\n--- FAIL: TestGuard\nfatal error: runtime failure']:
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
                    (0, '', False), (1, '=== RUN   TestGuard\n    guard_test.go:42: guard admitted forbidden input\n--- FAIL: TestGuard (100s)', False)
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

    def load_fake(self):
        loader = SourceFileLoader('fake_sudo', str(Path(__file__).with_name('demo-fake-sudo')))
        fake_spec = importlib.util.spec_from_loader(loader.name, loader)
        fake = importlib.util.module_from_spec(fake_spec)
        loader.exec_module(fake)
        return fake

    def test_recording_failure_is_fatal(self):
        fake = self.load_fake()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'rendezvous'
            log = Path(tmp) / 'calls'
            with patch.object(runner, 'recording_dir', return_value=root), patch.object(fake, 'recording_dir', return_value=root), patch.dict(fake.os.environ, {}, clear=True):
                with self.assertRaisesRegex(ValueError, 'sentinel'):
                    with runner.recording_session(log):
                        (root / 'log-path').unlink()
                        self.assertEqual(fake.main(), 78)
                        runner.check_recording()
                self.assertFalse(root.exists())

    def test_log_failure_is_fatal(self):
        fake = self.load_fake()
        with tempfile.TemporaryDirectory() as tmp:
            root, log = Path(tmp) / 'rendezvous', Path(tmp) / 'calls'
            log.mkdir()  # Opening this as the append log must fail.
            with patch.object(runner, 'recording_dir', return_value=root), patch.object(fake, 'recording_dir', return_value=root), patch.dict(fake.os.environ, {}, clear=True):
                with self.assertRaisesRegex(ValueError, 'sentinel'):
                    with runner.recording_session(log):
                        self.assertEqual(fake.main(), 78)
                        runner.check_recording()

    def test_pointer_protection(self):
        fake = self.load_fake()
        with tempfile.TemporaryDirectory() as tmp:
            root, log = Path(tmp) / 'rendezvous', Path(tmp) / 'calls'
            with patch.object(runner, 'recording_dir', return_value=root), patch.object(fake, 'recording_dir', return_value=root):
                with runner.recording_session(log):
                    self.assertEqual(fake.protected_pointer(), str(log))
                    (root / 'log-path').chmod(0o644)
                    with self.assertRaisesRegex(RuntimeError, 'unsafe'):
                        fake.protected_pointer()
                    (root / 'log-path').chmod(0o600)
                    with self.assertRaises(FileExistsError):
                        with runner.recording_session(log):
                            pass

    def test_fake_cleared_environment_and_orphan(self):
        import subprocess
        import sys
        import time
        import json
        fake = str(Path(__file__).with_name('demo-fake-sudo').resolve())
        # Import the committed fake, substitute only the test rendezvous path.
        child = """import importlib.machinery, importlib.util, os, pathlib, sys, time, json
loader=importlib.machinery.SourceFileLoader('fake',sys.argv[1])
spec=importlib.util.spec_from_loader(loader.name,loader)
fake=importlib.util.module_from_spec(spec);loader.exec_module(fake)
root=pathlib.Path(sys.argv[2]);fake.recording_dir=lambda:root
parent=int(sys.argv[4])
deadline=time.monotonic()+5
if parent:
 while os.getppid()==parent and time.monotonic()<deadline: time.sleep(.01)
code=fake.main()
result=pathlib.Path(sys.argv[3]);tmp=result.with_suffix('.pending')
tmp.write_text(json.dumps([code,os.getppid()!=parent]));tmp.replace(result)
"""
        with tempfile.TemporaryDirectory() as tmp:
            root, log = Path(tmp) / 'rendezvous', Path(tmp) / 'calls'
            with patch.object(runner, 'recording_dir', return_value=root):
                with runner.recording_session(log):
                    result = Path(tmp) / 'result'
                    subprocess.run([sys.executable, '-c', child, fake, str(root), str(result), '0'], env={}, check=True, timeout=10)
                    self.assertEqual(json.loads(result.read_text())[0], 77)
                    result.unlink()
                    parent = """import os, subprocess, sys
subprocess.Popen([sys.executable,'-c',sys.argv[1],*sys.argv[2:],str(os.getpid())],env={},stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
"""
                    subprocess.run([sys.executable, '-c', parent, child, fake, str(root), str(result)], env={}, check=True, timeout=10)
                    deadline = time.monotonic() + 10
                    while not result.exists() and time.monotonic() < deadline:
                        time.sleep(.02)
                    self.assertTrue(result.exists(), 'orphan fake did not finish')
                    self.assertEqual(json.loads(result.read_text()), [77, True])
                    self.assertEqual(runner.log_count(log), 2)
                    runner.check_recording()

    def test_no_new_privileges(self):
        for status, accepted in [('Name: fixture\nNoNewPrivs:\t1\n', True),
                                 ('NoNewPrivs:\t0\n', False), ('Name: fixture\n', False)]:
            with patch.object(Path, 'read_text', return_value=status):
                if accepted:
                    runner.require_no_new_privs()
                else:
                    with self.assertRaisesRegex(ValueError, 'NoNewPrivs'):
                        runner.require_no_new_privs()

    def test_main_refuses_before_subprocess_without_nnp(self):
        with patch.object(runner.sys, 'platform', 'linux'), patch.object(runner, 'require_no_new_privs', side_effect=ValueError('NoNewPrivs required')), patch.object(runner, 'verify_fake') as verify, patch.object(runner, 'execute') as execute:
            with self.assertRaises(SystemExit) as caught:
                runner.main(['--disposable', '--logs', '/unused'])
            self.assertEqual(caught.exception.code, 2)
            verify.assert_not_called()
            execute.assert_not_called()

    def test_assertion_must_belong_to_selected_failure(self):
        plant = dict(kills=dict(run='^TestGuard$'), expect='forbidden')
        cases = [
            ('=== RUN   TestOther\n    other_test.go:1: forbidden\n--- FAIL: TestOther\n=== RUN   TestGuard\n    guard_test.go:2: unrelated\n--- FAIL: TestGuard', 'invalid'),
            ('=== RUN   TestGuard\n=== RUN   TestGuard/control\n    guard_test.go:1: forbidden\n    --- PASS: TestGuard/control\n    guard_test.go:2: unrelated\n--- FAIL: TestGuard', 'invalid'),
            ('=== RUN   TestGuard\n=== RUN   TestGuard/bad\n    guard_test.go:1: forbidden\n    --- FAIL: TestGuard/bad\n--- FAIL: TestGuard', 'killed'),
            ('=== RUN   TestGuard\n    guard_test.go:1: forbidden\n--- FAIL: TestGuard', 'killed'),
            ('=== RUN   TestOther\n    other_test.go:1: forbidden\n--- FAIL: TestGuard', 'invalid'),
        ]
        for output, expected in cases:
            self.assertEqual(runner.classify(1, output, False, plant), expected)
        plant['kills']['run'] = '^TestGuard$/bad$'
        self.assertEqual(runner.classify(0, '--- PASS: TestGuard/good\n--- PASS: TestGuard', False, plant), 'invalid')
        self.assertEqual(runner.classify(1, '=== RUN   TestGuard/good\n    guard_test.go:1: forbidden\n    --- FAIL: TestGuard/good\n--- FAIL: TestGuard', False, plant), 'invalid')

    def test_nested_child_output_is_not_parent_evidence(self):
        plant = dict(kills=dict(run='^TestSealAllDescriptors$'), expect='unsealed inherited descriptor 3')
        template = """=== RUN   TestSealAllDescriptors
    guards_extra_linux_test.go:78: PARENT
        --- FAIL: TestSealAllDescriptors (0.00s)
            guards_extra_linux_test.go:46: unsealed inherited descriptor 3
        FAIL
--- FAIL: TestSealAllDescriptors (0.00s)
"""
        self.assertEqual(runner.classify(1, template.replace('PARENT', 'unsealed inherited descriptor 3'), False, plant), 'killed')
        self.assertEqual(runner.classify(1, template.replace('PARENT', 'descriptor seal child failed: exit status 1'), False, plant), 'invalid')
        # A verbose child also cannot replace the active parent's RUN block.
        verbose = template.replace('        --- FAIL:', '        === RUN   TestOther\n        --- FAIL:')
        self.assertEqual(runner.classify(1, verbose.replace('PARENT', 'unsealed inherited descriptor 3'), False, plant), 'killed')
        self.assertEqual(runner.classify(1, verbose.replace('PARENT', 'child failed'), False, plant), 'invalid')
        self.assertEqual(runner.classify(0, '        --- PASS: TestSealAllDescriptors', False, plant), 'invalid')

    def test_normal_subtest_depth(self):
        plant = dict(kills=dict(run='^TestGuard$/bad$'), expect='forbidden')
        output = """=== RUN   TestGuard
=== RUN   TestGuard/bad
    guard_test.go:10: forbidden
--- FAIL: TestGuard (0.01s)
    --- FAIL: TestGuard/bad (0.01s)
"""
        self.assertEqual(runner.classify(1, output, False, plant), 'killed')
        self.assertEqual(runner.classify(1, output.replace('    --- FAIL: TestGuard/bad', '        --- FAIL: TestGuard/bad'), False, plant), 'invalid')
        self.assertEqual(runner.classify(1, output.replace('=== RUN   TestGuard/bad', '        === RUN   TestGuard/bad'), False, plant), 'invalid')
        self.assertEqual(runner.classify(0, '--- PASS: TestGuard\n    --- PASS: TestGuard/bad', False, plant), 'survived')
        # Slash characters in one t.Run name do not create another nesting level.
        plant['kills']['run'] = '^TestGuard$'
        slash_name = output.replace('TestGuard/bad', 'TestGuard/bad/empty')
        self.assertEqual(runner.classify(1, slash_name, False, plant), 'killed')
        nested = slash_name.replace('=== RUN   TestGuard/bad/empty', '=== RUN   TestGuard/bad\n=== RUN   TestGuard/bad/empty').replace('    --- FAIL: TestGuard/bad/empty', '        --- FAIL: TestGuard/bad/empty')
        self.assertEqual(runner.classify(1, nested, False, plant), 'killed')


    def test_disposable_required(self):
        with self.assertRaises(SystemExit) as caught:
            runner.main(['--logs', '/unused'])
        self.assertEqual(caught.exception.code, 2)


if __name__ == '__main__':
    unittest.main()
