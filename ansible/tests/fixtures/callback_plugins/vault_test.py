"""Remove test wait time and record task outcomes."""

import json
import os
from pathlib import Path

from ansible.plugins.callback import CallbackBase


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = "aggregate"
    CALLBACK_NAME = "vault_test"
    CALLBACK_NEEDS_ENABLED = True

    def v2_playbook_on_task_start(self, task, is_conditional):
        # Keep production retry counts and task bodies; remove test wait time.
        task.delay = 0
        if task.action == "ansible.builtin.pause":
            task.args["seconds"] = 0

    def record(self, result, status):
        root = Path(os.environ["VAULT_TEST_ROOT"])
        event = {"name": result._task.get_name(), "status": status}
        with (root / "tasks.jsonl").open("a") as stream:
            stream.write(json.dumps(event) + "\n")

    def v2_runner_on_ok(self, result):
        self.record(result, "ok")

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self.record(result, "failed")

    def v2_runner_on_skipped(self, result):
        self.record(result, "skipped")
