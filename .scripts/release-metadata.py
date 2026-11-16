#!/usr/bin/env python3
"""Canonical ASCII JSON release metadata; no signature establishes origin."""
import hashlib
import json
import pathlib
import re
import sys


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True) + "\n").encode("ascii")


def digest(path):
    if not path.is_file() or path.is_symlink():
        raise ValueError("missing-or-symlinked-artifact")
    return hashlib.sha256(path.read_bytes()).hexdigest()


def fragment(root, names):
    return canonical({"schema": "release-set-fragment/1", "artifacts": [
        {"name": name, "sha256": digest(root / name)} for name in sorted(names)]})


def run():
    mode, directory, *args = sys.argv[1:]
    root = pathlib.Path(directory)
    if mode == "create":
        commit, version, epoch = args
        if not re.fullmatch(r"[0-9a-f]{40,64}", commit) or version != "go1.26.10" or not epoch.isdecimal():
            raise ValueError("invalid-build-inputs")
        provenance = {
            "commit": commit, "go_version": version, "SOURCE_DATE_EPOCH": int(epoch),
            "flags": ["-mod=readonly", "-p=2", "-trimpath", "-buildvcs=false", "-ldflags=-buildid="],
            "environment": {"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": ["amd64", "arm64"],
                            "GOAMD64": "v1", "GOARM64": "v8.0", "GOEXPERIMENT": "",
                            "GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off"},
        }
        (root / "provenance.json").write_bytes(canonical(provenance))
        names = sorted(p.name for p in root.glob("*_linux_*")) + ["provenance.json"]
        for name in names:
            if name != "provenance.json" and not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*_linux_(amd64|arm64)", name):
                raise ValueError("invalid-artifact-name")
        (root / "SHA256SUMS").write_text("".join(f"{digest(root / name)}  {name}\n" for name in sorted(names)), encoding="ascii")
        (root / "release-set.json").write_bytes(fragment(root, names + ["SHA256SUMS"]))
    elif mode == "check":
        if args:
            raise ValueError("unexpected-arguments")
        digest(root / "SHA256SUMS")
        digest(root / "release-set.json")
        names = []
        checksum_lines = []
        for line in (root / "SHA256SUMS").read_text(encoding="ascii").splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  (provenance\.json|[A-Za-z0-9][A-Za-z0-9._+-]*_linux_(amd64|arm64))", line)
            if not match:
                raise ValueError("invalid-checksum-entry")
            expected, name = match[1], match[2]
            if name in names:
                raise ValueError("duplicate-checksum-entry")
            if digest(root / name) != expected:
                raise ValueError("artifact-digest-mismatch")
            names.append(name)
            checksum_lines.append(f"{expected}  {name}\n")
        if "provenance.json" not in names or not any("_linux_" in n for n in names):
            raise ValueError("incomplete-manifest")
        if (root / "SHA256SUMS").read_bytes() != "".join(checksum_lines).encode("ascii"):
            raise ValueError("noncanonical-checksums")
        if names != sorted(names):
            raise ValueError("noncanonical-checksums")
        if (root / "release-set.json").read_bytes() != fragment(root, names + ["SHA256SUMS"]):
            raise ValueError("release-set-mismatch-or-noncanonical")
    else:
        raise ValueError("unknown-mode")


if __name__ == "__main__":
    try:
        run()
    except (ValueError, OSError, UnicodeError) as error:
        print(f"release metadata: {error}", file=sys.stderr)
        sys.exit(1)
