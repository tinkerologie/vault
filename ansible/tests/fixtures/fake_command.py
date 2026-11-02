#!/usr/bin/env python3
"""Fake controller commands; record real argv and never invoke SSH or SCP."""

import json
import os
import sys
from pathlib import Path

root = Path(os.environ["VAULT_TEST_ROOT"])
scenario = os.environ["VAULT_TEST_SCENARIO"]
program = Path(sys.argv[0]).name
args = sys.argv[1:]

with (root / "commands.jsonl").open("a") as stream:
    stream.write(json.dumps({"program": program, "args": args}) + "\n")

opens_connection = program == "scp" or (program == "ssh" and "-M" in args)
if opens_connection and scenario == "authentication_failure":
    sys.exit("fixture: Permission denied (publickey).")

if program == "ssh":
    if "-M" in args:
        Path(args[args.index("-S") + 1]).touch()
    print("ok")  # Ansible's own connection reads this as remote output.
elif program == "curl":
    print("101")
elif program == "go":
    if scenario == "included_failure":
        sys.exit("fixture MCP verification failed")
    print("MCP streamable HTTP OK: fixture")
elif program not in ("scp", "playwright-cli"):
    # Includes sshpass, which the tests also assert was never run.
    sys.exit("Unexpected controller command")
