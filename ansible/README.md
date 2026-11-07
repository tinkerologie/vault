# Vault Ansible Deployment

Ansible playbook for deploying, verifying, and managing the Vault plugin on Unraid servers.

## Setup

1. Copy the inventory template:

   ```bash
   cp inventory.yml.example inventory.yml
   ```

2. Edit `inventory.yml` with your Unraid server details.

3. Set up SSH key login. Ansible connects with a key only and never with a password:

   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519   # skip if you already have a key
   cat ~/.ssh/id_ed25519.pub
   ```

   In the Unraid web UI, paste the public key under **Users > root > SSH authorized keys**.
   Unraid keeps `/root/.ssh` on the flash drive at `/boot/config/ssh/root`, so the key
   survives reboots. Then set `ansible_ssh_private_key_file` in `inventory.yml` to the private
   key. Use a key without a passphrase or load it into `ssh-agent`, because connections run
   with `BatchMode=yes` and cannot prompt.

The first connection adds the server's host key to `~/.ssh/known_hosts`
(`StrictHostKeyChecking=accept-new`); later connections fail if that key changes.

Deploy, verify, uninstall and replica runs stop before connecting if the key is unset or
the file is missing, or if `ansible_password` is still set. `make build` needs no key.

### Moving from password login

Remove `ansible_password` and the `PubkeyAuthentication=no` and
`PreferredAuthentications=password` options from `inventory.yml` if present.
Then do step 3. An interrupted run from an older release can leave a password file.
To delete these files, use `rm -f "${TMPDIR:-/tmp}"/vault-sshpass-*`.
You can disable password login on the server separately.

## Usage

```bash
# Full deployment: build → deploy → verify
ansible-playbook -i inventory.yml ansible.yml

# Build only
ansible-playbook -i inventory.yml ansible.yml --tags build

# Deploy only (assumes binary exists)
ansible-playbook -i inventory.yml ansible.yml --tags deploy

# Verify endpoints
ansible-playbook -i inventory.yml ansible.yml --tags verify

# Verify using a specific stopped VM for the cold-backup smoke path
ansible-playbook -i inventory.yml ansible.yml --tags verify \
   -e verify_vm_backup_item_name="Fedora" \
   -e verify_vm_backup_mode=cold

# Verify VM restore over the existing VM definition
ansible-playbook -i inventory.yml ansible.yml --tags verify \
   -e verify_vm_restore_job_id=71 \
   -e verify_vm_restore_item_name="Home Assistant"

# Verify both in-place restore and deleted-VM restore/start
ansible-playbook -i inventory.yml ansible.yml --tags verify \
   -e verify_vm_restore_job_id=71 \
   -e verify_vm_restore_item_name="Home Assistant" \
   -e verify_vm_restore_delete_and_restore=true

# Uninstall plugin
ansible-playbook -i inventory.yml ansible.yml --tags uninstall

# Full lifecycle: uninstall → build → deploy → verify
ansible-playbook -i inventory.yml ansible.yml --tags redeploy

# Deploy without running tests
ansible-playbook -i inventory.yml ansible.yml --tags deploy --skip-tags tests

# Uninstall with config backup
ansible-playbook -i inventory.yml ansible.yml --tags uninstall -e create_backup=true
```

## Local Regression Tests

Use Python 3.11 or newer and a virtual environment. From the repository root:

```bash
python3 -m venv /tmp/vault-ansible-tests
source /tmp/vault-ansible-tests/bin/activate
python -m pip install -r ansible/tests/requirements.txt
make test-ansible
```

The tests run the deploy and verify roles with `-vvv --diff` and test keys. A test connection replaces the remote commands. The tests substitute local SSH, SCP, curl, Go, and Playwright commands. A temporary loopback HTTP server handles endpoint requests. The tests do not need an inventory or an Unraid host.

The tests cover these conditions:

- Key-only options on all 13 SCP paths (including `rc.vault`), the SSH tunnel, and the Ansible connection
- No calls to `sshpass`
- Pre-flight errors in every connecting role for an unset key, a missing key file, or a stored password
- API key removal from output
- Authentication errors, an unreachable host, and verification errors

The verify role reports endpoint errors and continues. Test callbacks remove retry delays from the tests.

CI runs `make test-ansible` in the **Ansible SSH key handling** job. The existing
Codecov configuration excludes `ansible/**`, so Codecov's patch percentage does
not measure these role tests.

Standard uninstall removes Vault-managed traces, including the binary, UI assets,
service script, database, config, logs, hybrid database artifacts, and stale
staging directories. Backup data stored in configured storage destinations is
preserved.

If you run uninstall with `create_backup=true`, Ansible saves a copy of the
Vault config directory before cleanup. That mode intentionally leaves the backup
copy behind.

## VM Backup Smoke Selection

The verify role can auto-discover a VM backup candidate, but it also supports
explicit selection when you want deterministic validation of a specific path.

- `verify_vm_backup_item_name`: Name of the VM to use for the VM backup smoke test.
- `verify_vm_backup_mode`: One of `auto`, `cold`, or `snapshot`.

Use `cold` only with a VM that is already `shutoff`, `shutdown`, or `paused`.
The verify task refuses a forced cold run against a running VM so the smoke test
stays non-destructive by default.

## VM Restore Smoke Selection

The verify role also supports opt-in VM restore validation against an existing
job that already has restore points.

- `verify_vm_restore_job_id`: Job ID whose restore points should be used.
- `verify_vm_restore_item_name`: VM name to restore from that job.
- `verify_vm_restore_delete_and_restore`: When `true`, run a second destructive
  restore check after undefining the VM and moving its disk files into a
  temporary safety directory. The safety directory is deleted on success and
  preserved on failure.
