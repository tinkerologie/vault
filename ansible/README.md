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

# Uninstall plugin
ansible-playbook -i inventory.yml ansible.yml --tags uninstall

# Full lifecycle: uninstall → build → deploy → verify
ansible-playbook -i inventory.yml ansible.yml --tags redeploy

# Deploy without running tests
ansible-playbook -i inventory.yml ansible.yml --tags deploy --skip-tags tests

# Uninstall with config backup
ansible-playbook -i inventory.yml ansible.yml --tags uninstall -e create_backup=true
```
