"""Offline checks; only synthetic repositories and executables are used."""

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("selfhost_cli", Path(__file__).with_name("selfhost-cli.py"))
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


class SelfhostCLITest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="multica-selfhost-cli-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "Synthetic test")
        self.git("config", "user.email", "synthetic@example.test")
        self.git("remote", "add", "origin", "https://github.com/dunzic/multica-worker.git")
        (self.repo / "seed.txt").write_text("synthetic source\n")
        self.commit("upstream")
        upstream = self.git("rev-parse", "HEAD")
        self.manifest_path = self.repo / "server/internal/buildinfo/distribution.json"
        self.manifest_path.parent.mkdir(parents=True)
        self.manifest = {
            "schema_version": 1, "distribution": "dunzic/multica-worker",
            "upstream": {"repository": "multica-ai/multica", "version": "0.6.2",
                         "release_commit": upstream, "commit": upstream},
            "minimum_cli_version": "0.6.2", "required_cli_capabilities": ["test-capability"],
            "api_requirements": {"test": "supported"},
        }
        self.save_manifest()
        self.commit("fork baseline")
        self.git("tag", "-a", "v0.6.6", "-m", "synthetic release")

    def git(self, *args):
        return cli.git(self.repo, *args)

    def commit(self, message):
        self.git("add", ".")
        self.git("commit", "-qm", message)

    def save_manifest(self):
        self.manifest_path.write_text(json.dumps(self.manifest))

    def candidate(self, **changes):
        meta = cli.build_metadata(self.repo)
        report = {"version": meta["version"], "build_commit": meta["commit"],
                  "distribution": meta["distribution"], "capabilities": ["test-capability"],
                  "os": "linux", "arch": "amd64"}
        report.update(changes)
        binary = self.root / "candidate"
        binary.write_text("#!/usr/bin/env python3\nimport sys\n"
                          "assert '--check-compatibility' in sys.argv\n"
                          "assert '--expected-version' in sys.argv\n"
                          + "print(" + repr(json.dumps(report)) + ")\n")
        binary.chmod(0o755)
        return binary

    def test_exact_tag(self):
        m = cli.build_metadata(self.repo)
        self.assertEqual((m["version"], m["commits_since_tag"]), ("0.6.6", 0))

    def test_private_derivative_retains_full_source_identity(self):
        (self.repo / "change.txt").write_text("reviewed change\n")
        self.commit("follow-up")
        m = cli.build_metadata(self.repo)
        self.assertEqual(m["version"], "0.6.6+selfhost.1.g" + self.git("rev-parse", "HEAD")[:9])
        self.assertEqual(m["commit"], self.git("rev-parse", "HEAD"))

    def test_missing_tag_fails(self):
        self.git("tag", "-d", "v0.6.6")
        with self.assertRaises(subprocess.CalledProcessError):
            cli.build_metadata(self.repo)

    def test_malformed_tags_fail(self):
        self.git("tag", "-d", "v0.6.6")
        for tag in ("v0.6.6-rc.1", "v00.6.6"):
            self.git("tag", tag)
            with self.assertRaises(ValueError):
                cli.build_metadata(self.repo)
            self.git("tag", "-d", tag)

    def test_dirty_tracked_source_fails(self):
        (self.repo / "seed.txt").write_text("unreviewed\n")
        with self.assertRaisesRegex(ValueError, "uncommitted"):
            cli.build_metadata(self.repo)

    def test_untracked_server_source_fails(self):
        (self.repo / "server/untracked.go").write_text("package unreviewed\n")
        with self.assertRaisesRegex(ValueError, "Untracked server"):
            cli.build_metadata(self.repo)

    def test_unrelated_operator_notes_do_not_change_binary(self):
        (self.repo / "operator-notes.txt").write_text("notes\n")
        self.assertEqual(cli.build_metadata(self.repo)["version"], "0.6.6")

    def test_wrong_origin_fails(self):
        self.git("remote", "set-url", "origin", "https://github.com/multica-ai/multica.git")
        with self.assertRaisesRegex(ValueError, "owned fork"):
            cli.build_metadata(self.repo)

    def test_unverified_upstream_fails(self):
        self.manifest["upstream"]["commit"] = "f" * 40
        self.save_manifest()
        self.commit("bad baseline")
        with self.assertRaises(subprocess.CalledProcessError):
            cli.build_metadata(self.repo)

    def test_floor_above_tag_fails(self):
        self.manifest["minimum_cli_version"] = "0.7.0"
        self.save_manifest()
        self.commit("higher floor")
        with self.assertRaisesRegex(ValueError, "below the recorded minimum"):
            cli.build_metadata(self.repo)

    def test_floor_below_upstream_fails(self):
        self.manifest["minimum_cli_version"] = "0.1.0"
        self.save_manifest()
        self.commit("invalid floor")
        with self.assertRaisesRegex(ValueError, "below the absorbed upstream"):
            cli.build_metadata(self.repo)

    def test_candidate_validates(self):
        result = cli.check_binary(self.repo, self.candidate(), "linux", "amd64")
        self.assertEqual(result["version"], "0.6.6")
        self.assertEqual(len(result["sha256"]), 64)

    def test_bad_candidate_metadata_fails(self):
        for change in ({"version": "main-123456789"}, {"build_commit": "f" * 40},
                       {"os": "darwin"}, {"arch": "arm64"}, {"distribution": {}},
                       {"capabilities": []}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                cli.check_binary(self.repo, self.candidate(**change), "linux", "amd64")

    def test_builtin_check_failure_fails_closed(self):
        binary = self.candidate()
        binary.write_text("#!/bin/sh\nexit 1\n")
        with self.assertRaisesRegex(ValueError, "built-in"):
            cli.check_binary(self.repo, binary, "linux", "amd64")


if __name__ == "__main__":
    unittest.main()
