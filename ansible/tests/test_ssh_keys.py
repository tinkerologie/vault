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


REQUIRED_OPTIONS = (
    "IdentitiesOnly=yes",
    "BatchMode=yes",
    "StrictHostKeyChecking=accept-new",
)
FORBIDDEN_OPTIONS = (
    "StrictHostKeyChecking=no",
    "UserKnownHostsFile=/dev/null",
    "PubkeyAuthentication=no",
    "PreferredAuthentications=password",
)
# Tilde and space together prove the path is expanded and quoted.
KEY_SETTING = "~/ssh keys/id_fixture"
PASSWORD_VARIABLES = ("ansible_password", "ansible_ssh_pass", "ansible_ssh_password")
PREFLIGHT_MESSAGES = {
    "password": "A password is still set in the inventory",
    "unset": "ansible_ssh_private_key_file is not set in the inventory",
    "missing": "does not exist on this machine",
}


class Run:
    def __init__(self, rc, output, commands, tasks, remote, port, key):
        self.rc = rc
        self.output = output
        self.commands = commands
        self.tasks = tasks
        self.remote = remote
        self.port = port
        self.key = key


class SSHKeyTests(unittest.TestCase):
    def execute(self, role=None, scenario="success", overrides=None, removed=(), real_playbook_tags=None):
        """Run a role, or the real ansible.yml with tags, against the fakes."""
        with tempfile.TemporaryDirectory(prefix="vault-ansible-test-") as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            key = root / "ssh keys" / "id_fixture"
            key.parent.mkdir()
            key.write_text("synthetic key material\n")
            key.chmod(0o600)
            api_key = "synthetic-api-key-" + uuid.uuid4().hex
            for program in ("sshpass", "ssh", "scp", "curl", "go", "playwright-cli"):
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
            hostname = "unraid" if real_playbook_tags else root.name
            socket = Path(f"/tmp/vault-verify-{hostname}.sock")
            settings = dict(
                ansible_ssh_private_key_file=KEY_SETTING,
                ansible_host="fixture.invalid",
                ansible_user="fixture",
                api_key=api_key,
                api_port=24085,
                bind_address="127.0.0.1",
                verify_local_port=server.server_port,
            )
            settings.update(overrides or {})
            for name in removed:
                del settings[name]
            inventory = {
                "all": {
                    "hosts": {
                        hostname: {"ansible_connection": "vault_test", **settings},
                        "localhost": {
                            "ansible_connection": "local",
                            "ansible_python_interpreter": sys.executable,
                        },
                    }
                }
            }
            (root / "inventory.yml").write_text(yaml.safe_dump(inventory))
            if real_playbook_tags:
                playbook = ANSIBLE / "ansible.yml"
                options = ["--tags", real_playbook_tags]
            else:
                variables = yaml.safe_load((ANSIBLE / "group_vars" / "all.yml").read_text())
                variables.update(version="test", plugin_binary=str(root / "vault-linux-amd64"))
                for name in ("api_port", "bind_address", "verify_local_port"):
                    del variables[name]
                playbook = root / "playbook.yml"
                playbook.write_text(yaml.safe_dump([{
                    "name": "SSH key regression",
                    "hosts": hostname,
                    "gather_facts": False,
                    "vars": variables,
                    "roles": [role],
                }]))
                options = []
            (root / "ansible.cfg").write_text("[defaults]\nretry_files_enabled = false\n")
            env = os.environ.copy()
            # Ansible debug can bypass no_log; this test exercises supported verbosity.
            for key_name in list(env):
                if key_name.startswith("ANSIBLE_"):
                    del env[key_name]
            env.update(
                VAULT_TEST_ROOT=str(root),
                VAULT_TEST_SCENARIO=scenario,
                HOME=str(root),
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
                     str(playbook), "-vvv", "--diff", *options],
                    env=env, capture_output=True, text=True, timeout=180,
                )
                output = completed.stdout + completed.stderr
                # Redact even synthetic credentials before an assertion can print output.
                safe_output = output.replace(api_key, "[REDACTED]")
                self.assertTrue(api_key not in output, "API key leaked in task output")
                self.assertFalse(socket.exists(), "Verification socket was not removed")

                def read_events(name):
                    path = root / name
                    return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

                commands = read_events("commands.jsonl")
                self.assertEqual([e for e in commands if e["program"] == "sshpass"], [], "sshpass was invoked")
                return Run(completed.returncode, safe_output, commands, read_events("tasks.jsonl"),
                           read_events("remote.jsonl"), server.server_port, str(key))
            finally:
                server.shutdown()
                server.server_close()
                thread.join()
                socket.unlink(missing_ok=True)

    def run_role(self, role, scenario="success", **kwargs):
        return self.execute(role, scenario, **kwargs)

    def task(self, tasks, name):
        matches = [event for event in tasks if event["name"].endswith(name)]
        self.assertEqual(len(matches), 1, f"Expected one task: {name}")
        return matches[0]

    def assert_key_only(self, event, key):
        args = event["args"]
        self.assertEqual(args[args.index("-i") + 1], key, args)
        for option in REQUIRED_OPTIONS:
            self.assertTrue(option in args, f"{option} missing: {args}")
        for option in FORBIDDEN_OPTIONS:
            self.assertFalse(option in args, f"{option} present: {args}")

    def assert_stopped_by_preflight(self, run, reason):
        self.assertNotEqual(run.rc, 0, run.output)
        self.assertTrue(PREFLIGHT_MESSAGES[reason] in run.output, run.output)
        self.assertEqual(run.commands, [], "A local command ran after the preflight failed")
        self.assertEqual(run.remote, [], "A remote command ran after the preflight failed")

    def test_deploy_probes_the_daemons_actual_bind(self):
        # The health check and bind report must use the address rc.vault
        # passed to the daemon, not re-parse vault.cfg with other defaults.
        tasks = yaml.safe_load((ANSIBLE / "roles" / "deploy" / "tasks" / "main.yml").read_text())

        def find(items, name):
            for item in items or []:
                if item.get("name") == name:
                    return item
                found = find(item.get("block"), name) or find(item.get("rescue"), name) or find(item.get("always"), name)
                if found:
                    return found
            return None

        for name in ("Wait for API to be ready", "Read effective daemon bind"):
            task = find(tasks, name)
            self.assertIsNotNone(task, name)
            script = task["ansible.builtin.raw"]
            self.assertTrue("/proc/$PID/cmdline" in script, name)
            self.assertTrue("--addr=" in script, name)
            self.assertFalse("vault.cfg" in script, name)

    def test_deploy_all_scp_paths(self):
        run = self.run_role("deploy")
        self.assertEqual(run.rc, 0, run.output)
        uploads = [event for event in run.commands if event["program"] == "scp"]
        self.assertEqual(len(uploads), 13)  # 12 plugin files + rc.vault (#468)
        for upload in uploads:
            self.assert_key_only(upload, run.key)
        destinations = [event["args"][-1].split(":", 1)[1] for event in uploads]
        plugin = "/usr/local/emhttp/plugins/vault/"
        self.assertEqual(destinations, [
            "/usr/local/sbin/vault", plugin + "Vault.page", plugin + "Backups.page",
            plugin + "README.md", plugin + "include/api.php", plugin + "include/control.php",
            plugin + "include/app.php", plugin + "include/embed-host.js", plugin + "include/proxy.php", plugin + "include/apply.sh",
            plugin + "event/", plugin + "ui/", "/etc/rc.d/rc.vault",
        ])
        # rc.vault ships from plugin/rc.vault, the script the .plg installs (#468).
        self.assertTrue(uploads[-1]["args"][-2].endswith("/plugin/rc.vault"))
        self.assertEqual(uploads[-2]["args"][0], "-r")
        self.assertTrue(uploads[-3]["args"][-3].endswith("/plugin/event/started"))
        self.assertTrue(uploads[-3]["args"][-2].endswith("/plugin/event/stopping_svcs"))

    def test_deploy_removes_legacy_ui_and_never_uploads_it(self):
        tasks = yaml.safe_load((ANSIBLE / "roles" / "deploy" / "tasks" / "main.yml").read_text())

        def walk(items):
            for item in items or []:
                yield item
                for key in ("block", "rescue", "always"):
                    yield from walk(item.get(key))

        cleanup = next(t for t in walk(tasks) if t.get("name") == "Clean up old sub-page files")
        script = cleanup["ansible.builtin.raw"]
        for name in ("VaultJobs", "VaultRestore", "VaultStorage", "VaultHistory", "VaultSettings"):
            self.assertTrue("{{ plugin_dir }}/" + name + ".page" in script, name)
        self.assertTrue("{{ plugin_dir }}/assets" in script, script)

        run = self.run_role("deploy")
        self.assertEqual(run.rc, 0, run.output)
        uploads = [event for event in run.commands if event["program"] == "scp"]
        self.assertTrue(uploads)
        for event in uploads:
            joined = " ".join(event["args"])
            self.assertFalse("plugin/assets" in joined, joined)
            for name in ("VaultJobs", "VaultRestore", "VaultStorage", "VaultHistory", "VaultSettings"):
                self.assertFalse(name in joined, joined)

    def test_verify_tunnel_and_included_tasks(self):
        run = self.run_role("verify")
        self.assertEqual(run.rc, 0, run.output)
        ssh = [event for event in run.commands if event["program"] == "ssh"]
        tunnels = [event for event in ssh if "-M" in event["args"]]
        self.assertEqual(len(tunnels), 1)
        args = tunnels[0]["args"]
        self.assert_key_only(tunnels[0], run.key)
        self.assertTrue(f"127.0.0.1:{run.port}:127.0.0.1:24085" in args, args)
        self.assertTrue("-f" in args, args)
        self.assertTrue("-N" in args, args)
        self.assertTrue("ExitOnForwardFailure=yes" in args, args)
        self.assertTrue("Endpoints: 3/3 passed, Ping: 200" in run.output, run.output)
        self.assertTrue("MCP streamable HTTP OK:" in run.output, run.output)
        self.assertTrue("Playwright UI verification completed: passed" in run.output, run.output)
        stops = [event for event in ssh if "exit" in event["args"]]
        self.assertEqual(len(stops), 1)
        self.assert_key_only(stops[0], run.key)

    def test_api_key_is_still_sent_but_censored(self):
        # execute() asserts the key never reaches output at -vvv.
        run = self.run_role("verify")
        self.assertEqual(run.rc, 0, run.output)
        by_program = {event["program"]: " ".join(event["args"]) for event in run.commands}
        for program, flag in (("curl", "X-API-Key: synthetic-api-key-"), ("go", "-api-key synthetic-api-key-")):
            self.assertTrue(flag in by_program[program], program)

    def test_ansible_connection_is_key_only(self):
        # Real ssh connection plugin and real ansible.cfg, with ssh itself faked.
        with tempfile.TemporaryDirectory(prefix="vault-ansible-test-") as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            fake = root / "bin" / "ssh"
            fake.write_text(f"#!{sys.executable}\n" + (FIXTURES / "fake_command.py").read_text().split("\n", 1)[1])
            fake.chmod(0o700)
            key = root / "id_fixture"
            key.write_text("synthetic key material\n")
            # An outdated inventory must not be able to weaken the connection.
            inventory = {"all": {"hosts": {"fixture": {
                "ansible_host": "fixture.invalid",
                "ansible_user": "fixture",
                "ansible_ssh_private_key_file": str(key),
                "ansible_ssh_common_args": "-o StrictHostKeyChecking=no -o BatchMode=no",
            }}}}
            (root / "inventory.yml").write_text(yaml.safe_dump(inventory))
            (root / "playbook.yml").write_text(yaml.safe_dump([{
                "name": "Connection regression",
                "hosts": "fixture",
                "gather_facts": False,
                "tasks": [{"name": "Remote command", "ansible.builtin.raw": "echo ok"}],
            }]))
            env = {k: v for k, v in os.environ.items() if not k.startswith("ANSIBLE_")}
            env.update(
                VAULT_TEST_ROOT=str(root),
                VAULT_TEST_SCENARIO="success",
                HOME=str(root),
                PATH=str(root / "bin") + os.pathsep + env["PATH"],
                ANSIBLE_CONFIG=str(ANSIBLE / "ansible.cfg"),
                ANSIBLE_NOCOLOR="1",
            )
            completed = subprocess.run(
                [shutil.which("ansible-playbook"), "-i", str(root / "inventory.yml"), str(root / "playbook.yml")],
                env=env, capture_output=True, text=True, timeout=120,
            )
            self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
            events = [json.loads(line) for line in (root / "commands.jsonl").read_text().splitlines()]
            self.assertTrue(events)
            for event in events:
                args = event["args"]
                self.assertEqual(event["program"], "ssh")
                self.assertTrue(f'IdentityFile="{key}"' in args, args)
                self.assertTrue("PasswordAuthentication=no" in args, args)
                self.assertTrue(any(a.startswith("ControlPath=/tmp/.vault-key-") for a in args), args)
                # ssh uses the first value it sees for an option.
                values = [a for a in args if a.split("=")[0] in ("IdentitiesOnly", "BatchMode", "StrictHostKeyChecking")]
                for option in REQUIRED_OPTIONS:
                    name = option.split("=")[0]
                    self.assertEqual(next(v for v in values if v.startswith(name + "=")), option, args)

    def test_preflight_rejects_a_stored_password(self):
        secret = "synthetic-password-" + uuid.uuid4().hex
        for role in ("deploy", "verify"):
            for variable in PASSWORD_VARIABLES:
                with self.subTest(role=role, variable=variable):
                    run = self.run_role(role, overrides={variable: secret})
                    self.assert_stopped_by_preflight(run, "password")
                    self.assertFalse(secret in run.output, "Password leaked in output")

    def test_preflight_rejects_an_unset_key(self):
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                run = self.run_role(role, removed=("ansible_ssh_private_key_file",))
                self.assert_stopped_by_preflight(run, "unset")
            with self.subTest(role=role, value="empty"):
                run = self.run_role(role, overrides={"ansible_ssh_private_key_file": ""})
                self.assert_stopped_by_preflight(run, "unset")

    def test_preflight_rejects_a_missing_key_file(self):
        missing = "~/no-such-key-" + uuid.uuid4().hex
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                run = self.run_role(role, overrides={"ansible_ssh_private_key_file": missing})
                self.assert_stopped_by_preflight(run, "missing")
                self.assertFalse(missing.removeprefix("~/") in run.output, "Key path echoed by the preflight")

    def test_real_playbook_runs_preflight_for_every_connecting_tag(self):
        for tags in ("deploy", "verify", "uninstall", "replica"):
            with self.subTest(tags=tags):
                run = self.execute(real_playbook_tags=tags, removed=("ansible_ssh_private_key_file",))
                self.assert_stopped_by_preflight(run, "unset")

    def test_real_playbook_build_tag_does_not_need_a_key(self):
        listed = subprocess.run(
            [shutil.which("ansible-playbook"), "-i", "localhost,", str(ANSIBLE / "ansible.yml"),
             "--list-tasks", "--tags", "build"],
            capture_output=True, text=True, timeout=60,
            env={**os.environ, "ANSIBLE_ROLES_PATH": str(ANSIBLE / "roles")},
        )
        self.assertEqual(listed.returncode, 0, listed.stdout + listed.stderr)
        self.assertFalse("ssh_preflight" in listed.stdout, listed.stdout)

    def test_no_password_login_remains_in_the_ansible_config(self):
        paths = [ANSIBLE / name for name in ("ansible.yml", "ansible.cfg", "inventory.yml.example")]
        paths += list((ANSIBLE / "group_vars").rglob("*.yml")) + list((ANSIBLE / "roles").rglob("*.yml"))
        for path in paths:
            text = path.read_text()
            for forbidden in (*FORBIDDEN_OPTIONS, "sshpass", "ansible_password:", "{{ ansible_password", "/tmp/.vault-deploy-"):
                self.assertFalse(forbidden in text, f"{forbidden} in {path}")

    def test_authentication_failures_keep_diagnostics(self):
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                run = self.run_role(role, "authentication_failure")
                self.assertNotEqual(run.rc, 0, run.output)
                self.assertTrue("Permission denied (publickey)." in run.output, run.output)
                if role == "deploy":
                    self.assertTrue("fixture daemon diagnostic" in run.output, run.output)
                    self.assertEqual(self.task(run.tasks, "Fail with deployment error")["status"], "failed")

    def test_included_verification_failure_runs_always(self):
        run = self.run_role("verify", "included_failure")
        self.assertNotEqual(run.rc, 0, run.output)
        self.assertTrue("fixture MCP verification failed" in run.output, run.output)
        self.assertTrue(any("exit" in event["args"] for event in run.commands if event["program"] == "ssh"))

    def test_endpoint_errors_remain_visible_and_run_always(self):
        run = self.run_role("verify", "endpoint_failure")
        # The role intentionally reports endpoint errors and continues verification.
        self.assertEqual(run.rc, 0, run.output)
        self.assertTrue("Endpoints: 0/3 passed, Ping: 503" in run.output, run.output)
        self.assertTrue("FAILED: Health (HTTP 503), Jobs (HTTP 503), Storage (HTTP 503)" in run.output, run.output)
        self.assertTrue(any("exit" in event["args"] for event in run.commands if event["program"] == "ssh"))

    def test_unreachable_server_fails_fast(self):
        for role in ("deploy", "verify"):
            with self.subTest(role=role):
                run = self.run_role(role, "unreachable")
                self.assertNotEqual(run.rc, 0, run.output)
                self.assertTrue("fixture host unreachable" in run.output, run.output)
                self.assertFalse(any(e["program"] in ("scp", "ssh") for e in run.commands))
                self.assertFalse(any(e["name"].endswith("Stop existing service") for e in run.tasks))


if __name__ == "__main__":
    unittest.main()
