# Triage labels

This repo uses the default canonical triage label vocabulary. Each label
string is equal to its role name.

| Role            | Label             | Meaning                                           |
| --------------- | ----------------- | ------------------------------------------------- |
| Needs triage    | `needs-triage`    | New and unreviewed; awaiting a first pass         |
| Needs info      | `needs-info`      | Blocked on the reporter; missing details or repro |
| Ready for agent | `ready-for-agent` | Well-specified; an AI agent can implement it      |
| Ready for human | `ready-for-human` | Well-specified; needs human judgment or access    |
| Won't fix       | `wontfix`         | Closed as not planned                             |

The `triage` skill applies these labels when sorting incoming issues. If the
labels do not exist on the repo yet, create them once with
`gh label create <name>`.
