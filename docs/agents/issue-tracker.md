# Issue tracker

Issues and specs live in GitHub Issues on `tinkerologie/vault`.
Use the gh CLI from this repository to read and update issues.
Create issues through the GitHub issue forms below.

## Conventions

- Follow `AGENTS.md`'s issue forms and Coding Plan requirements.
- Read: `gh issue view <number> --json number,title,body,labels,comments`
- List: `gh issue list --state open`
- Create: open the [GitHub issue forms](https://github.com/tinkerologie/vault/issues/new/choose)
  and complete Bug report (`01-bug-report.yml`) or Enhancement request
  (`02-enhancement-request.yml`). GitHub CLI does not support these YAML forms.
  If browser access is unavailable, ask a human to submit the completed form.
- For multiline edits or comments on an existing issue, write the content
  to a temporary file and use `gh issue edit <number> --body-file <file>`
  or `gh issue comment <number> --body-file <file>`.
- Apply or remove labels with `gh issue edit`.
- Publish specs as issues; fetch tickets with `gh issue view`.
- Use `docs/agents/triage-labels.md` for triage roles.
- Link child issues through GitHub sub-issues when available; otherwise
  use a parent task list and a `Part of #<parent>` reference.
- Record dependencies using GitHub issue dependencies when available;
  otherwise use `Blocked by: #<number>` references.

## Pull requests as a triage surface

**PRs as a request surface: no.**
