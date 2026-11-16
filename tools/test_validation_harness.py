#!/usr/bin/env python3
"""Failure-path checks for validation capture and benchmark summaries."""

import json
import importlib.util
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parent.parent
TOOLING = ROOT / "tools" / "validation_tooling.py"
CAPTURE = ROOT / "tools" / "validation_capture.py"
BENCHMARK = ROOT / "tools" / "interface_benchmark.py"


class HarnessTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.test_tree = self.root / "suite-tests"
        self.test_tree.mkdir()
        (self.test_tree / "case.txt").write_text("frozen test bytes\n", encoding="utf-8")
        self.conditions = self.root / "conditions.json"
        self.conditions.write_text(json.dumps({"target": "smoke", "conditions": "fixed"}), encoding="utf-8")
        self.prepared = self.root / "prepared.json"
        subprocess.run([
            sys.executable, str(TOOLING), "--output", str(self.prepared),
            "--tool-name", "python smoke", "--installation-notes", "preinstalled test interpreter",
            "--executable", sys.executable, "--version-command", f'"{sys.executable}" --version',
            "--source-tree", str(self.test_tree), "--precondition", "mount=not-required",
        ], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        prepared = json.loads(self.prepared.read_text(encoding="utf-8"))
        self.assertEqual(prepared["version"], prepared["version_check"]["stdout"].strip())
        self.assertTrue(prepared["version_check"]["stdout_sha256"])
        self.assertEqual(len(prepared["source_trees"][0]["sha256"]), 64)

    def tearDown(self):
        self.temp.cleanup()

    def capture(self, name, command, manifest=None, conditions=None):
        out = self.root / name
        proc = subprocess.run([
            sys.executable, str(CAPTURE), "--output", str(out), "--suite", "smoke",
            "--target", "local", "--tooling-manifest", str(manifest or self.prepared),
            "--conditions-json", str(conditions or self.conditions), "--", *command,
        ], stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        result_path = out / "result.json"
        self.assertTrue(result_path.exists(), proc.stderr.decode(errors="replace"))
        self.assertTrue((out / "stdout.log").exists())
        self.assertTrue((out / "stderr.log").exists())
        self.assertTrue((out / "preflight.log").exists())
        return proc, json.loads(result_path.read_text(encoding="utf-8")), out

    def test_completed_and_nonzero_exit_are_distinct(self):
        ok, result, _ = self.capture("success", [sys.executable, "-c", "print('ok')"])
        self.assertEqual(ok.returncode, 0)
        self.assertEqual(result["status"], "completed")
        failed, result, out = self.capture("nonzero", [sys.executable, "-c", "import sys; print('failure'); sys.exit(7)"])
        self.assertEqual(failed.returncode, 7)
        self.assertEqual(result["status"], "command_failed")
        self.assertEqual(result["exit_code"], 7)
        self.assertEqual((out / "stdout.log").read_text(encoding="utf-8").strip(), "failure")

    def test_process_launch_failure_is_launch_failed(self):
        broken = self.root / "broken-suite"
        broken.write_text("#!/missing/interpreter/for/harness-test\nexit 0\n", encoding="utf-8")
        broken.chmod(0o755)
        subprocess.run([
            sys.executable, str(TOOLING), "--output", str(self.prepared), "--tool-name", "broken",
            "--installation-notes", "test", "--executable", str(broken), "--executable", sys.executable,
            "--version-command", f'"{sys.executable}" --version', "--source-tree", str(self.test_tree),
        ], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        proc, result, _ = self.capture("launch", [str(broken)])
        self.assertEqual(proc.returncode, 127)
        self.assertEqual(result["status"], "launch_failed")
        self.assertEqual(result["failure_kind"], "environment_launch_error")
        self.assertEqual(result["launch_error"]["type"], "FileNotFoundError")

    def test_preflight_manifest_conditions_tree_and_version_failures_are_recorded(self):
        malformed = self.root / "malformed.json"
        malformed.write_text("{", encoding="utf-8")
        proc, result, out = self.capture("bad-manifest", [sys.executable, "-c", "pass"], manifest=malformed)
        self.assertEqual(proc.returncode, 125)
        self.assertEqual(result["status"], "preflight_failed")
        self.assertEqual(result["preflight"]["error"]["type"], "JSONDecodeError")
        self.assertIn("JSONDecodeError", (out / "preflight.log").read_text(encoding="utf-8"))
        self.assertTrue((out / "tooling-manifest.input.json").exists())

        missing = self.root / "missing-manifest.json"
        proc, result, _ = self.capture("missing-manifest", [sys.executable, "-c", "pass"], manifest=missing)
        self.assertEqual(proc.returncode, 125)
        self.assertEqual(result["status"], "preflight_failed")
        self.assertEqual(result["preflight"]["error"]["type"], "FileNotFoundError")

        invalid_conditions = self.root / "invalid-conditions.json"
        invalid_conditions.write_text("[]", encoding="utf-8")
        proc, result, _ = self.capture("bad-conditions", [sys.executable, "-c", "pass"], conditions=invalid_conditions)
        self.assertEqual(proc.returncode, 125)
        self.assertEqual(result["status"], "preflight_failed")

        missing_conditions = self.root / "missing-conditions.json"
        proc, result, _ = self.capture("missing-conditions", [sys.executable, "-c", "pass"], conditions=missing_conditions)
        self.assertEqual(proc.returncode, 125)
        self.assertEqual(result["preflight"]["error"]["type"], "FileNotFoundError")

        malformed_conditions = self.root / "malformed-conditions.json"
        malformed_conditions.write_text("{", encoding="utf-8")
        proc, result, out = self.capture("malformed-conditions", [sys.executable, "-c", "pass"], conditions=malformed_conditions)
        self.assertEqual(proc.returncode, 125)
        self.assertEqual(result["preflight"]["error"]["type"], "JSONDecodeError")
        self.assertTrue((out / "conditions.input.json").exists())

        (self.test_tree / "case.txt").write_text("changed after preparation\n", encoding="utf-8")
        proc, result, _ = self.capture("changed-tree", [sys.executable, "-c", "pass"])
        self.assertEqual(proc.returncode, 125)
        self.assertIn("tree content changed", result["preflight"]["error"]["message"])

        (self.test_tree / "case.txt").write_text("frozen test bytes\n", encoding="utf-8")
        manifest = json.loads(self.prepared.read_text(encoding="utf-8"))
        manifest["executables"][0]["path"] = str(self.root / "missing-executable")
        bad_executable = self.root / "bad-executable.json"
        bad_executable.write_text(json.dumps(manifest), encoding="utf-8")
        proc, result, _ = self.capture("bad-executable", [sys.executable, "-c", "pass"], manifest=bad_executable)
        self.assertEqual(proc.returncode, 125)
        self.assertEqual(result["status"], "preflight_failed")

        manifest = json.loads(self.prepared.read_text(encoding="utf-8"))
        manifest["version_check"]["stdout_sha256"] = "0" * 64
        bad_version = self.root / "bad-version.json"
        bad_version.write_text(json.dumps(manifest), encoding="utf-8")
        proc, result, out = self.capture("changed-version", [sys.executable, "-c", "pass"], manifest=bad_version)
        self.assertEqual(proc.returncode, 125)
        self.assertIn("version/source revision differs", result["preflight"]["error"]["message"])
        self.assertTrue((out / "version-check.stdout.log").exists())

    def test_partial_failed_samples_are_separate_from_successful_summary(self):
        spec = importlib.util.spec_from_file_location("benchmark", BENCHMARK)
        benchmark = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(benchmark)
        success, failed = benchmark.summarize_records([
            {"status": "ok", "operations": {"read_ns": 100}},
            {"status": "failed", "operations": {"read_ns": 900}},
        ], ["read_ns"])
        self.assertEqual(success["read_ns"]["samples"], 1)
        self.assertEqual(success["read_ns"]["median_ns"], 100)
        self.assertEqual(failed["read_ns"]["samples"], 1)
        self.assertEqual(failed["read_ns"]["median_ns"], 900)


if __name__ == "__main__":
    unittest.main()
