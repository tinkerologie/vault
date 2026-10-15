"""Replace every remote raw command; never connect to or modify an Unraid host."""

import json
import os
from pathlib import Path

from ansible.plugins.connection import ConnectionBase


class Connection(ConnectionBase):
    transport = "vault_test"
    has_pipelining = True

    def _connect(self):
        self._connected = True
        return self

    def exec_command(self, cmd, in_data=None, sudoable=True):
        root = Path(os.environ["VAULT_TEST_ROOT"])
        with (root / "remote.jsonl").open("a") as stream:
            stream.write(json.dumps({"cmd": cmd}) + "\n")
        # Both bind-reading tasks need output; other raw tasks only need rc=0.
        if "TARGET_VALUE" in cmd:
            output = "127.0.0.1:24085\n127.0.0.1\n"
        elif "BIND_VALUE" in cmd:
            output = "127.0.0.1:24085\n"
        elif "tail -50" in cmd:
            output = "fixture daemon diagnostic\n"
        else:
            output = "ok\n"
        return 0, output.encode(), b""

    def put_file(self, in_path, out_path):
        raise AssertionError("Unexpected remote file transfer")

    def fetch_file(self, in_path, out_path):
        raise AssertionError("Unexpected remote file fetch")

    def close(self):
        self._connected = False
