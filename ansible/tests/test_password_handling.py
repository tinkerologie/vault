"""Execute the real deploy/verify roles with local fakes at external boundaries."""

import http.server
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
import uuid
from pathlib import Path

import yaml


TESTS = Path(__file__).resolve().parent
ANSIBLE = TESTS.parent
FIXTURES = TESTS / "fixtures"


class PasswordHandlingTests(unittest.TestCase):
    def run_role(self, role, scenario="success", stale_password_file=False):
        with tempfile.TemporaryDirectory(prefix="vault-ansible-test-") as directory:
            root = Path(directory)
            (root / "tmp").mkdir()
            (root / "bin").mkdir()
            if stale_password_file:
                # Left behind by a run whose host dropped mid-deploy.
                stale = root / "tmp" / "vault-sshpass-stale"
                stale.write_text("old")
                two_hours_ago = stale.stat().st_mtime - 7200
                os.utime(stale, (two_hours_ago, two_hours_ago))
            password = "synthetic-password-" + uuid.uuid4().hex + " ' $ ;"
            (root / "expected-password").write_text(password)
            (root / "expected-password").chmod(0o600)
            for program in ("sshpass", "ssh", "curl", "go", "playwright-cli"):
                command = root / "bin" / program
                command.write_text(
                    f"#!{sys.executable}\n"
                    + (FIXTURES / "fake_command.py").read_text().split("\n", 1)[1]
                )
                command.chmod(0o700)

            class Handler(http.server.BaseHTTPRequestHandler):
                def do_GET(self):
                    self.send_response(503 if scenario == "endpoint_failure" else 200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(b"{}")

                def log_message(self, *args):
                    pass

            server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            hostname = root.name
            socket = Path(f"/tmp/vault-verify-{hostname}.sock")
            inventory = {
                "all": {
                    "hosts": {
                        hostname: {"ansible_connection": "vault_test"},
                        "localhost": {
                            "ansible_connection": "local",
                            "ansible_python_interpreter": sys.executable,
                        },
                    }
                }
            }
            variables = yaml.safe_load((ANSIBLE / "group_vars" / "all.yml").read_text())
            variables.update(
                ansible_password=password,
                ansible_host="fixture.invalid",
                ansible_user="fixture",
                version="test",
                api_port=24085,
                bind_address="127.0.0.1",
                verify_local_port=server.server_port,
                plugin_binary=str(root / "vault-linux-amd64"),
            )
            play = [{
                "name": "Password handling regression",
                "hosts": hostname,
                "gather_facts": False,
                "vars": variables,
                "roles": [role],
            }]
            (root / "inventory.yml").write_text(yaml.safe_dump(inventory))
            (root / "playbook.yml").write_text(yaml.safe_dump(play))
            (root / "playbook.yml").chmod(0o600)
            (root / "ansible.cfg").write_text("[defaults]\nretry_files_enabled = false\n")
            env = os.environ.copy()
            # Ansible debug can bypass no_log; this test exercises supported verbosity.
            for key in list(env):
                if key.startswith("ANSIBLE_") or key == "SSHPASS":
                    del env[key]
            env.update(
                VAULT_TEST_ROOT=str(root),
                VAULT_TEST_SCENARIO=scenario,
                TMPDIR=str(root / "tmp"),
                PATH=str(root / "bin") + os.pathsep + env["PATH"],
                ANSIBLE_CONFIG=str(root / "ansible.cfg"),
                ANSIBLE_ROLES_PATH=str(ANSIBLE / "roles"),
                ANSIBLE_CONNECTION_PLUGINS=str(FIXTURES / "connection_plugins"),
                ANSIBLE_CALLBACK_PLUGINS=str(FIXTURES / "callback_plugins"),
                ANSIBLE_CALLBACKS_ENABLED="vault_test",
                ANSIBLE_NOCOLOR="1",
            )
            try:
                completed = subprocess.run(
                    [shutil.which("ansible-playbook"), "-i", str(root / "inventory.yml"),
                     str(root / "playbook.yml"), "-vvv", "--diff"],
                    env=env, capture_output=True, text=True, timeout=180,
                )
                output = completed.stdout + completed.stderr
                # Redact even synthetic credentials before an assertion can print output.
                safe_output = output.replace(password, "[REDACTED]")
                self.assertTrue(password not in output, "Password leaked in task output")
                self.assertEqual(
                    list((root / "tmp").glob("vault-sshpass-*")), [],
                    "Password file was not removed by always",
                )
                self.assertFalse(socket.exists(), "Verification socket was not removed")

                def read_events(name):
                    path = root / name
                    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

                commands = read_events("commands.jsonl")
                tasks = read_events("tasks.jsonl")
                for command in commands:
                    self.assertTrue(password not in json.dumps(command), "Password leaked in argv")
                    if command["program"] == "sshpass":
                        self.assertEqual(command["mode"], 0o600)
                        self.assertTrue(command["password_matches"], "Password contents changed")
                        self.assertTrue(command["argv_safe"], "Password leaked in process list")
                        self.assertTrue(command["environment_safe"], "SSHPASS unexpectedly set")
                        self.assertFalse(Path(command["path"]).exists())
                return completed.returncode, safe_output, commands, tasks, server.server_port
            finally:
                server.shutdown()
                server.server_close()
                thread.join()
                socket.unlink(missing_ok=True)

    def task(self, tasks, name):
        matches = [event for event in tasks if event["name"].endswith(name)]
        self.assertEqual(len(matches), 1, f"Expected one task: {name}")
        return matches[0]

    def assert_password_written(self, tasks):
        created = self.task(tasks, "Create private SSH password file")
        written = self.task(tasks, "Store SSH password in the private file")
        self.assertEqual(created["status"], "ok")
        self.assertEqual(created["files"][0]["mode"], 0o600)
        self.assertEqual(written["status"], "ok")
        self.assertTrue(written["no_log"])
        self.assertEqual(written["files"][0]["mode"], 0o600)
        self.assertTrue(written["files"][0]["password_matches"])
        self.assertEqual(self.task(tasks, "Remove SSH password file")["status"], "ok")

    def test_deploy_all_scp_paths(self):
        rc, output, commands, tasks, _ = self.run_role("deploy")
        self.assertEqual(rc, 0, output)
        self.assert_password_written(tasks)
        uploads = [event for event in commands if event["program"] == "sshpass"]
        self.assertEqual(len(uploads), 12)  # 11 plugin files + rc.vault (#468)
        self.assertEqual(len({event["path"] for event in uploads}), 1)
        self.assertTrue(all(event["args"][2] == "scp" for event in uploads))
        destinations = [event["args"][-1].split(":", 1)[1] for event in uploads]
        plugin = "/usr/local/emhttp/plugins/vault/"
        self.assertEqual(destinations, [
            "/usr/local/sbin/vault", plugin + "Vault.page", plugin + "Backups.page",
            plugin + "README.md", plugin + "include/api.php", plugin + "include/control.php",
            plugin + "include/app.php", plugin + "include/proxy.php", plugin + "include/apply.sh",
            plugin + "event/", plugin + "ui/", "/etc/rc.d/rc.vault",
        ])
        # rc.vault ships from plugin/rc.vault, the script the .plg installs (#468).
        self.assertTrue(uploads[-1]["args"][-2].endswith("/plugin/rc.vault"))
        self.assertEqual(uploads[-2]["args"][3], "-r")
        self.assertTrue(uploads[-3]["args"][-3].endswith("/plugin/event/started"))
        self.assertTrue(uploads[-3]["args"][-2].endswith("/plugin/event/stopping_svcs"))

    def test_verify_tunnel_and_included_tasks(self):
        rc, output, commands, tasks, port = self.run_role("verify")
        self.assertEqual(rc, 0, output)
        self.assert_password_written(tasks)
        tunnels = [event for event in commands if event["program"] == "sshpass"]
        self.assertEqual(len(tunnels), 1)
        args = tunnels[0]["args"]
        self.assertEqual(args[2:4], ["ssh", "-M"])
        self.assertTrue(f"127.0.0.1:{port}:127.0.0.1:24085" in args, args)
        self.assertTrue("-f" in args[3:], args)
        self.assertTrue("-N" in args, args)
        self.assertTrue("ExitOnForwardFailure=yes" in args, args)
        self.assertTrue("Endpoints: 3/3 passed, Ping: 200" in output, output)
        self.assertTrue("MCP streamable HTTP OK:" in output, output)
        self.assertTrue("Playwright UI verification completed: passed" in output, output)
        self.assertTrue(any(event["program"] == "ssh" and "exit" in event["args"] for event in commands))

    def test_authentication_failures_keep_diagnostics_and_cleanup(self):
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                rc, output, commands, tasks, _ = self.run_role(role, "authentication_failure")
                self.assertNotEqual(rc, 0, output)
                self.assert_password_written(tasks)
                self.assertTrue("Permission denied, please try again." in output, output)
                self.assertTrue(any(event["program"] == "sshpass" for event in commands))
                if role == "deploy":
                    self.assertTrue("fixture daemon diagnostic" in output, output)
                    self.assertEqual(self.task(tasks, "Fail with deployment error")["status"], "failed")

    def test_tempfile_failures_skip_cleanup_for_undefined_path(self):
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                rc, output, commands, tasks, _ = self.run_role(role, "tempfile_failure")
                self.assertNotEqual(rc, 0, output)
                self.assertEqual(self.task(tasks, "Create private SSH password file")["status"], "failed")
                self.assertEqual(self.task(tasks, "Remove SSH password file")["status"], "skipped")
                self.assertFalse(any(event["program"] == "sshpass" for event in commands))
                self.assertNotIn("'sshpass_file' is undefined", output)

    def test_copy_failures_are_censored_and_cleanup_defined_path(self):
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                rc, output, commands, tasks, _ = self.run_role(role, "copy_failure")
                self.assertNotEqual(rc, 0, output)
                written = self.task(tasks, "Store SSH password in the private file")
                self.assertEqual(written["status"], "failed")
                self.assertTrue(written["no_log"])
                self.assertTrue("censored" in output, output)
                self.assertEqual(self.task(tasks, "Remove SSH password file")["status"], "ok")
                self.assertFalse(any(event["program"] == "sshpass" for event in commands))

    def test_included_verification_failure_runs_always(self):
        rc, output, commands, tasks, _ = self.run_role("verify", "included_failure")
        self.assertNotEqual(rc, 0, output)
        self.assert_password_written(tasks)
        self.assertTrue("fixture MCP verification failed" in output, output)
        self.assertTrue(any(event["program"] == "ssh" for event in commands))

    def test_endpoint_errors_remain_visible_and_run_always(self):
        rc, output, _, tasks, _ = self.run_role("verify", "endpoint_failure")
        # The role intentionally reports endpoint errors and continues verification.
        self.assertEqual(rc, 0, output)
        self.assert_password_written(tasks)
        self.assertTrue("Endpoints: 0/3 passed, Ping: 503" in output, output)
        self.assertTrue("FAILED: Health, Jobs, Storage" in output, output)


    def test_unreachable_server_fails_fast_without_leaking(self):
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                rc, output, commands, tasks, _ = self.run_role(role, "unreachable")
                self.assertNotEqual(rc, 0, output)
                self.assertTrue("fixture host unreachable" in output, output)
                self.assertFalse(any(event["program"] == "sshpass" for event in commands))
                if role == "deploy":
                    # Fails before the password file exists, and before any
                    # other remote task.
                    self.assertFalse(any(e["name"].endswith("Create private SSH password file") for e in tasks))
                    self.assertFalse(any(e["name"].endswith("Stop existing service") for e in tasks))
                else:
                    self.assertEqual(self.task(tasks, "Remove SSH password file")["status"], "ok")

    def test_deploy_removes_stale_password_files(self):
        rc, output, _, tasks, _ = self.run_role("deploy", stale_password_file=True)
        self.assertEqual(rc, 0, output)
        # run_role also asserts no vault-sshpass-* file survives.
        self.assertEqual(self.task(tasks, "Delete stale SSH password files")["status"], "ok")


if __name__ == "__main__":
    unittest.main()
