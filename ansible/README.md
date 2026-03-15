# Vault Ansible Deployment

Ansible playbook for deploying, verifying, and managing the Vault plugin on Unraid servers.

## Setup

1. Copy the inventory template:

   ```bash
   cp inventory.yml.example inventory.yml
   ```

2. Edit `inventory.yml` with your Unraid server details.

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
