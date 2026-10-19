#!/usr/bin/env python3
"""Derive and verify private source builds without publishing a release."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


def git(repo, *args):
    return subprocess.check_output(
        ["git", *args], cwd=repo, text=True, stderr=subprocess.PIPE
    ).strip()


def stable_version(value):
    if not isinstance(value, str) or not re.fullmatch(
        r"v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", value
    ):
        raise ValueError("A stable semantic version is required")
    return tuple(int(part) for part in value.removeprefix("v").split("."))


def build_metadata(repo):
    repo = Path(repo).resolve()
    origin = git(repo, "remote", "get-url", "origin")
    if origin not in (
        "https://github.com/dunzic/multica-worker.git",
        "https://github.com/dunzic/multica-worker",
        "git@github.com:dunzic/multica-worker.git",
    ):
        raise ValueError("Self-host builds require the owned fork origin")
    if git(repo, "status", "--porcelain", "--untracked-files=no"):
        raise ValueError("Tracked source files have uncommitted changes")
    if git(repo, "ls-files", "--others", "--exclude-standard", "--", "server"):
        raise ValueError("Untracked server source would make build provenance ambiguous")
    commit = git(repo, "rev-parse", "HEAD")
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("Missing full source commit")
    manifest = json.loads((repo / "server/internal/buildinfo/distribution.json").read_text())
    if (
        manifest.get("schema_version") != 1
        or manifest.get("distribution") != "dunzic/multica-worker"
        or manifest.get("upstream", {}).get("repository") != "multica-ai/multica"
    ):
        raise ValueError("Invalid distribution baseline")
    upstream = manifest["upstream"]
    for value in (upstream["commit"], upstream["release_commit"]):
        if not re.fullmatch(r"[0-9a-f]{40}", value):
            raise ValueError("Invalid upstream commit")
    git(repo, "merge-base", "--is-ancestor", upstream["release_commit"], upstream["commit"])
    git(repo, "merge-base", "--is-ancestor", upstream["commit"], commit)
    minimum = stable_version(manifest["minimum_cli_version"])
    if minimum < stable_version(upstream["version"]):
        raise ValueError("Minimum CLI version is below the absorbed upstream baseline")
    tag = git(repo, "describe", "--tags", "--match", "v[0-9]*", "--abbrev=0", "HEAD")
    if stable_version(tag) < minimum:
        raise ValueError("Source tag is below the recorded minimum CLI version")
    tag_commit = git(repo, "rev-parse", tag + "^{commit}")
    git(repo, "merge-base", "--is-ancestor", tag_commit, commit)
    distance = int(git(repo, "rev-list", "--count", tag_commit + ".." + commit))
    version = tag.removeprefix("v")
    if distance:
        # Build metadata identifies a private derivative, not a new public tag.
        version += "+selfhost." + str(distance) + ".g" + commit[:9]
    return {
        "version": version,
        "commit": commit,
        "source_tag": tag,
        "tag_commit": tag_commit,
        "commits_since_tag": distance,
        "distribution": manifest,
    }


def check_binary(repo, binary, target_os, target_arch):
    metadata = build_metadata(repo)
    binary = Path(binary).resolve(strict=True)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError("Candidate must be an executable regular file")
    before = hashlib.sha256(binary.read_bytes()).hexdigest()
    result = subprocess.run(
        [str(binary), "version", "--check-compatibility", "--expected-version",
         metadata["version"], "--output", "json"],
        capture_output=True, text=True, timeout=30,
    )
    if result.returncode:
        raise ValueError("Candidate failed its built-in version compatibility check")
    report = json.loads(result.stdout)
    for key, expected in (
        ("version", metadata["version"]), ("build_commit", metadata["commit"]),
        ("distribution", metadata["distribution"]), ("os", target_os), ("arch", target_arch),
    ):
        if report.get(key) != expected:
            raise ValueError("Candidate metadata mismatch: " + key)
    required = metadata["distribution"]["required_cli_capabilities"]
    capabilities = report.get("capabilities")
    if not isinstance(capabilities, list) or not required or not all(c in capabilities for c in required):
        raise ValueError("Candidate is missing required CLI capabilities")
    if hashlib.sha256(binary.read_bytes()).hexdigest() != before:
        raise ValueError("Candidate changed during verification")
    return {**metadata, "sha256": before, "os": target_os, "arch": target_arch,
            "capabilities": capabilities}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("metadata", "check"))
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--os", default="linux")
    parser.add_argument("--arch", default="amd64")
    args = parser.parse_args()
    try:
        if args.action == "check":
            if args.binary is None:
                parser.error("check requires --binary")
            result = check_binary(args.repo, args.binary, args.os, args.arch)
        else:
            result = build_metadata(args.repo)
        print(json.dumps(result, sort_keys=True))
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        # Never echo candidate output or process environments into shared logs.
        parser.exit(1, "Self-host CLI validation failed: " + type(error).__name__ + ": "
                    + (str(error) if isinstance(error, (ValueError, KeyError)) else "inspect source history and local tooling") + "\n")


if __name__ == "__main__":
    main()
