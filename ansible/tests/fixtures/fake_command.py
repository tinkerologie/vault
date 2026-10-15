#!/usr/bin/env python3
"""Fake controller commands; record real argv and never invoke SSH or SCP."""

import json
import os
import stat
import subprocess
import sys
from pathlib import Path

root = Path(os.environ["VAULT_TEST_ROOT"])
scenario = os.environ["VAULT_TEST_SCENARIO"]
program = Path(sys.argv[0]).name
args = sys.argv[1:]
event = {"program": program, "args": args}

if program == "sshpass":
    if len(args) < 3 or args[0] != "-f" or args[2] not in ("scp", "ssh"):
        sys.exit("Expected sshpass -f with SCP or SSH")
    password_file = Path(args[1])
    password = (root / "expected-password").read_text()
    event.update(
        path=str(password_file),
        mode=stat.S_IMODE(password_file.stat().st_mode),
        password_matches=password_file.read_text() == password,
        argv_safe=password not in subprocess.check_output(["ps", "-eo", "args"], text=True),
        environment_safe="SSHPASS" not in os.environ,
    )

with (root / "commands.jsonl").open("a") as stream:
    stream.write(json.dumps(event) + "\n")

if program == "sshpass":
    if scenario == "authentication_failure":
        sys.exit("Permission denied, please try again.")
    if args[2] == "ssh":
        Path(args[args.index("-S") + 1]).touch()
elif program == "curl":
    print("101")
elif program == "go":
    if scenario == "included_failure":
        sys.exit("fixture MCP verification failed")
    print("MCP streamable HTTP OK: fixture")
elif program not in ("ssh", "playwright-cli"):
    sys.exit("Unexpected controller command")
