#!/usr/bin/env python3
"""Run intended mutation witnesses only on an explicitly disposable Linux host."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile


def apply(tree, plant):
    edits = [{k: plant[k] for k in ("file", "find", "replace")}] + plant.get("edits", [])
    for edit in edits:
        path = (tree / edit["file"]).resolve()
        if not path.is_relative_to(tree.resolve()):
            raise ValueError("edit escapes copied tree")
        source = path.read_text()
        if not edit["find"] or source.count(edit["find"]) != 1:
            raise ValueError("find must match exactly once: " + edit["file"])
        path.write_text(source.replace(edit["find"], edit["replace"], 1))


def classify(code, output, timed_out, plant):
    if timed_out:
        return "killed" if plant.get("timeout_kills") and plant.get("timeout_reason") else "invalid"
    if code == 0:
        pattern = plant["kills"]["run"].split("/")[0]
        passed = re.findall(r"--- PASS: ([^\s]+)", output)
        return "survived" if any(re.search(pattern, name.split("/")[0]) for name in passed) else "invalid"
    # Compilation errors, skips, unrelated process failure and panic-only output
    # are not test assertion evidence. The selected test must report a failure.
    names = re.findall(r"--- FAIL: ([^\s]+)", output)
    pattern = plant["kills"]["run"].split("/")[0]
    return "killed" if any(re.search(pattern, name.split("/")[0]) for name in names) else "invalid"


def execute(argv, cwd, timeout):
    proc = subprocess.Popen(argv, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            text=True, start_new_session=True)
    timed_out = False
    try:
        output, _ = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        os.killpg(proc.pid, signal.SIGKILL)
        output, _ = proc.communicate()
    finally:
        # Own process group only: failing mutants may leave fixture descendants.
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    return proc.returncode, output, timed_out


def copy_tree(source, target):
    shutil.copytree(source, target, symlinks=True,
                    ignore=shutil.ignore_patterns('.git', '.temp', '__pycache__', '*.pyc'))


def run_test(tree, plant, log, timeout):
    package = plant["kills"]["package"]
    if not package.startswith('./') or '..' in Path(package).parts:
        raise ValueError("package must be checkout-local")
    binary = tree.parent / 'witness.test'
    code, output, expired = execute(['go', 'test', '-c', '-o', str(binary), package], tree, 180)
    log.write_text('BUILD\n' + output)
    if code or expired:
        return 'invalid'
    code, output, expired = execute([str(binary), '-test.v', '-test.count=1',
                                    '-test.run=' + plant['kills']['run'],
                                    '-test.timeout=' + str(timeout) + 's'],
                                   tree / package[2:], timeout + 5)
    with log.open('a') as stream:
        stream.write('\nTEST\n' + output)
    # Go's own timeout is also invalid by default, even if it prints FAIL.
    expired = expired or 'panic: test timed out after' in output
    return classify(code, output, expired, plant)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--disposable', action='store_true')
    parser.add_argument('--only')
    parser.add_argument('--scratch-root', type=Path, default=Path.home())
    parser.add_argument('--logs', type=Path, required=True)
    parser.add_argument('--timeout', type=int, default=90)
    args = parser.parse_args(argv)
    if not args.disposable:
        parser.error('--disposable is required: mutants can reach unknown paths')
    if sys.platform != 'linux':
        parser.error('mutation execution requires a disposable Linux host')
    if args.timeout < 1:
        parser.error('timeout must be positive')
    source = Path(__file__).resolve().parent.parent
    scratch = args.scratch_root.resolve()
    if scratch.is_relative_to(source) or args.logs.resolve().is_relative_to(source):
        parser.error('scratch and logs must be outside the checkout')
    entries = json.loads((source / '.scripts/demo-launch-mutants.json').read_text())
    if args.only:
        entries = [p for p in entries if p['id'] == args.only]
        if not entries:
            parser.error('unknown plant ID')
    args.logs.mkdir(parents=True, exist_ok=True)
    # All selected unmutated controls precede every mutation, without retry.
    controls = {}
    with tempfile.TemporaryDirectory(prefix='demo-mutants-', dir=scratch) as root:
        root = Path(root)
        for plant in entries:
            key = (plant['kills']['package'], plant['kills']['run'])
            if key in controls:
                continue
            work = root / ('control-' + str(len(controls)))
            copy_tree(source, work)
            result = run_test(work, plant, args.logs / (plant['id'] + '-control.log'), args.timeout)
            controls[key] = result
            if result != 'survived':
                print('control failed: ' + plant['id'] + ' (' + result + ')', flush=True)
                return 1
            shutil.rmtree(work)
        counts = dict(killed=0, survived=0, invalid=0)
        for index, plant in enumerate(entries):
            work = root / ('plant-' + str(index))
            copy_tree(source, work)
            try:
                apply(work, plant)
                result = run_test(work, plant, args.logs / (plant['id'] + '.log'), args.timeout)
            except (ValueError, OSError) as error:
                (args.logs / (plant['id'] + '.log')).write_text(str(error))
                result = 'invalid'
            counts[result] += 1
            print(plant['id'] + ' ' + result, flush=True)
            shutil.rmtree(work)
        print(json.dumps(counts, sort_keys=True), flush=True)
        return 0 if counts['killed'] == len(entries) else 1


if __name__ == '__main__':
    raise SystemExit(main())
