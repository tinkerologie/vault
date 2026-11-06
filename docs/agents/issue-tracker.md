# Issue tracker

Issues and specs live in GitHub Issues on `tinkerologie/vault`.
Use the gh CLI from this repository.

## Conventions

- Follow `AGENTS.md`'s issue forms and Coding Plan requirements.
- Read: `gh issue view <number> --json number,title,body,labels,comments`
- List: `gh issue list --state open`
- Create a bug: `gh issue create --template 01-bug-report.yml`.
- Create an enhancement: `gh issue create --template 02-enhancement-request.yml`.
- For multiline bodies, write the content to a temporary file and use
  `--body-file`.
- Apply or remove labels with `gh issue edit`.
- Publish specs as issues; fetch tickets with `gh issue view`.
- Use `docs/agents/triage-labels.md` for triage roles.
- Link child issues through GitHub sub-issues when available; otherwise
  use a parent task list and a `Part of #<parent>` reference.
- Record dependencies using GitHub issue dependencies when available;
  otherwise use `Blocked by: #<number>` references.

## Pull requests as a triage surface

**PRs as a request surface: no.**
