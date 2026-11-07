"""Bounded runner controls. No Go toolchain or subprocesses are used."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

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
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'))
        self.assertEqual(runner.classify(1, '--- FAIL: TestGuard (0.1s)', False, plant), 'killed')
        for output in ['compile error', '--- SKIP: TestGuard', '--- FAIL: TestOther (0.1s)', 'panic: unrelated']:
            self.assertEqual(runner.classify(1, output, False, plant), 'invalid')
        self.assertEqual(runner.classify(0, '--- PASS: TestGuard (0.1s)', False, plant), 'survived')
        self.assertEqual(runner.classify(1, '--- FAIL: TestGuard', True, plant), 'invalid')
        plant.update(timeout_kills=True, timeout_reason='deliberate wait bound removal')
        self.assertEqual(runner.classify(1, '', True, plant), 'killed')

    def test_build_failure_is_invalid(self):
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'))
        with tempfile.TemporaryDirectory() as tmp:
            tree = Path(tmp) / 'tree'
            tree.mkdir()
            log = Path(tmp) / 'log'
            with patch.object(runner, 'execute', return_value=(1, 'build failed', False)) as execute:
                self.assertEqual(runner.run_test(tree, plant, log, 1), 'invalid')
                self.assertEqual(execute.call_count, 1)
            self.assertIn('build failed', log.read_text())

    def test_no_matching_control_is_invalid(self):
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'))
        for text in ['PASS', '--- SKIP: TestGuard', '--- PASS: TestOther']:
            self.assertEqual(runner.classify(0, text, False, plant), 'invalid')

    def test_entry_timeout(self):
        plant = dict(kills=dict(package='./internal/demolaunch', run='^TestGuard$'))
        with tempfile.TemporaryDirectory() as tmp:
            tree = Path(tmp) / 'tree'
            tree.mkdir()
            log = Path(tmp) / 'log'
            for override, expected in [(None, 90), (180, 180)]:
                if override is not None:
                    plant['timeout_seconds'] = override
                with patch.object(runner, 'execute', side_effect=[
                    (0, '', False), (1, '--- FAIL: TestGuard (100s)', False)
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

    def test_disposable_required(self):
        with self.assertRaises(SystemExit) as caught:
            runner.main(['--logs', '/unused'])
        self.assertEqual(caught.exception.code, 2)


if __name__ == '__main__':
    unittest.main()
