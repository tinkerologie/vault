---
applyTo: "**/*.go,web/src/**/*.js,web/src/**/*.svelte,plugin/**/*.php,plugin/**/*.sh,ansible/ansible.yml,ansible/roles/**/*.yml,.github/workflows/**/*.yml"
description: Scan modified first-party code with Aikido
---

# Aikido Security Scan

Run `aikido_full_scan` on added or modified first-party source code, including
generated source intended for commit, unless the user explicitly opts out.

- Send the complete content of each changed code file, using repository-relative
  paths.
- Do not send credentials, untracked configuration, build outputs, dependency
  or vendor artifacts, or unrelated files.
- Validate each finding against the source before changing code.
- Fix confirmed issues, then rescan the affected files.
- If Aikido is unavailable, report that limitation and continue with
  source-grounded security checks; do not claim an Aikido pass.
