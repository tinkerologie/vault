"""Inject filesystem failures and record task outcomes without recording secrets."""

import json
import os
import stat
from pathlib import Path

from ansible.plugins.callback import CallbackBase


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "aggregate"
    CALLBACK_NAME = "vault_test"
    CALLBACK_NEEDS_ENABLED = True

    def v2_playbook_on_task_start(self, task, is_conditional):
        root = Path(os.environ["VAULT_TEST_ROOT"])
        scenario = os.environ["VAULT_TEST_SCENARIO"]
        if scenario == "tempfile_failure" and task.action == "ansible.builtin.tempfile":
            task.args["path"] = str(root / "missing-directory")
        if scenario == "copy_failure" and task.action == "ansible.builtin.copy":
            task.args["dest"] = str(root / "missing-directory" / "password")
        # Keep production retry counts and task bodies; remove test wait time.
        task.delay = 0
        if task.action == "ansible.builtin.pause":
            task.args["seconds"] = 0

    def record(self, result, status):
        root = Path(os.environ["VAULT_TEST_ROOT"])
        event = {"name": result._task.get_name(), "status": status}
        if result._task.action in ("ansible.builtin.tempfile", "ansible.builtin.copy"):
            event["no_log"] = bool(result._task.no_log)
            event["files"] = [
                {
                    "path": str(path),
                    "mode": stat.S_IMODE(path.stat().st_mode),
                    "password_matches": path.read_bytes()
                    == (root / "expected-password").read_bytes(),
                }
                for path in (root / "tmp").glob("vault-sshpass-*")
            ]
        with (root / "tasks.jsonl").open("a") as stream:
            stream.write(json.dumps(event) + "\n")

    def v2_runner_on_ok(self, result):
        self.record(result, "ok")

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self.record(result, "failed")

    def v2_runner_on_skipped(self, result):
        self.record(result, "skipped")
