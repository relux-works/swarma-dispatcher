#!/usr/bin/env python3
"""Run intended mutation witnesses only on an explicitly disposable Linux host."""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time


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
    expect = plant.get("expect")
    if not isinstance(expect, str) or not expect:
        return "invalid"
    if timed_out:
        return "invalid"
    if 'panic:' in output or 'fatal error:' in output:
        return "invalid"
    selected = plant["kills"]["run"].split("/")
    def matches(name):
        parts = name.split("/")
        return len(parts) >= len(selected) and all(re.search(p, n) for p, n in zip(selected, parts))
    if code == 0:
        passed = re.findall(r"--- PASS: ([^\s]+)", output)
        return "survived" if any(matches(name) for name in passed) else "invalid"
    blocks = {}
    current = None
    for line in output.splitlines():
        event = re.match(r"^\s*=== (?:RUN|NAME|CONT)\s+(\S+)", line)
        if event:
            current = event.group(1)
            blocks.setdefault(current, [])
            continue
        end = re.match(r"^\s*--- (FAIL|PASS|SKIP): (\S+)", line)
        if end:
            kind, name = end.groups()
            if kind == "FAIL" and matches(name) and any(re.search(expect, a) for a in blocks.get(name, [])):
                return "killed"
            current = name.rpartition("/")[0] or None
            continue
        assertion = re.match(r"^\s*[^\s]+_test\.go:\d+: (.*)$", line)
        if current is not None and assertion:
            blocks[current].append(assertion.group(1))
    return "invalid"



def execute(argv, cwd, timeout):
    # Retain the leader as a zombie until its process group has been signalled:
    # the group ID cannot be reused before our reap. Never communicate()/poll()
    # first, since both can reap the leader.
    proc = subprocess.Popen(argv, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            text=True, start_new_session=True)
    chunks = []
    reader = threading.Thread(target=lambda: chunks.append(proc.stdout.read()), daemon=True)
    reader.start()
    deadline = time.monotonic() + timeout
    timed_out = False
    while True:
        observed = os.waitid(os.P_PID, proc.pid, os.WEXITED | os.WNOWAIT | os.WNOHANG)
        if observed is not None:
            break
        if time.monotonic() >= deadline:
            timed_out = True
            break
        time.sleep(0.01)
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    proc.wait()
    reader.join(timeout=5)
    if reader.is_alive():
        return proc.returncode, ''.join(chunks), True
    proc.stdout.close()
    return proc.returncode, ''.join(chunks), timed_out


SUDO_PATHS = ('/usr/bin/sudo', '/usr/bin/sudo-rs', '/usr/bin/sudo.ws',
              '/bin/sudo', '/usr/local/bin/sudo', '/usr/sbin/sudo')



def require_no_new_privs():
    if not re.search(r"^NoNewPrivs:\s+1$", Path('/proc/self/status').read_text(), re.MULTILINE):
        raise ValueError('NoNewPrivs: 1 is required')


def recording_dir():
    return Path('/tmp/demo-launch-fake-' + str(os.getuid()))


def check_recording():
    root = recording_dir()
    if not (root / 'healthy').is_file():
        raise ValueError('fake sudo recording failed: sentinel missing')


@contextmanager
def recording_session(log):
    if log is None:
        raise ValueError('--fake-sudo and --fake-log are required')
    root = recording_dir()
    # An existing rendezvous is never taken over, including after an interrupted run.
    root.mkdir(mode=0o700)
    try:
        for name, content in [('log-path', str(log.resolve())), ('healthy', 'healthy')]:
            fd = os.open(root / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
            with os.fdopen(fd, 'w') as stream:
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
        yield
        check_recording()
    finally:
        for name in ('log-path', 'healthy'):
            (root / name).unlink(missing_ok=True)
        root.rmdir()

def log_count(log):
    if not log.exists():
        return 0
    lines = log.read_text().splitlines()
    for line in lines:
        record = json.loads(line)
        if not isinstance(record.get('argv'), list) or not isinstance(record.get('cwd'), str):
            raise ValueError('invalid fake sudo log')
    return len(lines)


def verify_fake(fake, log, committed):
    if fake is None or log is None:
        raise ValueError('--fake-sudo and --fake-log are required')
    if fake.read_bytes() != committed.read_bytes():
        raise ValueError('fake sudo bytes differ from committed fake')
    expected = fake.stat()
    paths = set(SUDO_PATHS)
    found = shutil.which('sudo')
    if found:
        paths.add(found)
    if not Path('/usr/bin/sudo').exists():
        raise ValueError('/usr/bin/sudo must be the fake')
    for name in paths:
        path = Path(name)
        if path.exists() or path.is_symlink():
            actual = path.stat()
            if (actual.st_dev, actual.st_ino) != (expected.st_dev, expected.st_ino):
                raise ValueError('sudo path is not the fake: ' + name)
    os.environ['DEMO_FAKE_SUDO_LOG'] = str(log.resolve())
    before = log_count(log)
    result = subprocess.run(['/usr/bin/sudo', '-n', '/bin/true'], timeout=5,
                            capture_output=True, env=os.environ.copy())
    if result.returncode != 77 or log_count(log) != before + 1:
        raise ValueError('fake sudo self-test failed')


def copy_tree(source, target):
    shutil.copytree(source, target, symlinks=True,
                    ignore=shutil.ignore_patterns('.git', '.temp', '__pycache__', '*.pyc'))


def run_test(tree, plant, log, timeout):
    timeout = plant.get("timeout_seconds", timeout)
    if type(timeout) is not int or timeout < 1:
        raise ValueError("timeout_seconds must be a positive integer")
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
    parser.add_argument('--fake-sudo', type=Path)
    parser.add_argument('--fake-log', type=Path)
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
    try:
        require_no_new_privs()
        with recording_session(args.fake_log):
            return run_selected(args, parser)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        parser.error(str(error))


def run_selected(args, parser):
    source = Path(__file__).resolve().parent.parent
    try:
        verify_fake(args.fake_sudo, args.fake_log, source / '.scripts/demo-fake-sudo')
        check_recording()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        parser.error(str(error))
    scratch = args.scratch_root.resolve()
    if scratch.is_relative_to(source) or args.logs.resolve().is_relative_to(source):
        parser.error('scratch and logs must be outside the checkout')
    entries = json.loads((source / '.scripts/demo-launch-mutants.json').read_text())
    if args.only:
        entries = [p for p in entries if p['id'] == args.only]
        if not entries:
            parser.error('unknown plant ID')
    entries = [p for p in entries if not p.get('accepted_residue')]
    for plant in entries:
        if not plant.get('expect'): parser.error('missing expect: ' + plant['id'])
        re.compile(plant['expect'])
    args.logs.mkdir(parents=True, exist_ok=True)
    sudo_total = 0
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
            before = log_count(args.fake_log)
            result = run_test(work, plant, args.logs / (plant['id'] + '-control.log'), args.timeout)
            check_recording()
            calls = log_count(args.fake_log) - before
            sudo_total += calls
            print(plant['id'] + ' control ' + result + ' sudo_calls=' + str(calls), flush=True)
            controls[key] = result
            if result != 'survived':
                print('control failed: ' + plant['id'] + ' (' + result + ')', flush=True)
                print(json.dumps(dict(killed=0, survived=0, invalid=0, control_failed=True, sudo_calls=sudo_total), sort_keys=True), flush=True)
                return 1
            shutil.rmtree(work)
        counts = dict(killed=0, survived=0, invalid=0)
        for index, plant in enumerate(entries):
            work = root / ('plant-' + str(index))
            copy_tree(source, work)
            before = log_count(args.fake_log)
            try:
                apply(work, plant)
                result = run_test(work, plant, args.logs / (plant['id'] + '.log'), args.timeout)
            except (ValueError, OSError) as error:
                (args.logs / (plant['id'] + '.log')).write_text(str(error))
                result = 'invalid'
            counts[result] += 1
            check_recording()
            calls = log_count(args.fake_log) - before
            sudo_total += calls
            print(plant['id'] + ' ' + result + ' sudo_calls=' + str(calls), flush=True)
            shutil.rmtree(work)
        print(json.dumps(dict(counts, sudo_calls=sudo_total), sort_keys=True), flush=True)
        return 0 if counts['killed'] == len(entries) else 1


if __name__ == '__main__':
    raise SystemExit(main())
