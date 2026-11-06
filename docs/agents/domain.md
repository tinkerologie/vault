# Domain docs

Layout: single-context.

## Before exploring

- Read GLOSSARY.md at the repository root when present.
- Read relevant architecture decisions in docs/adr/.
- If these files are absent, proceed silently. Matt Pocock's
  `domain-modeling` skill creates them when terms or decisions are resolved.
- Read the existing CONTEXT.md for established vocabulary and module
  context. When adding GLOSSARY.md, keep vocabulary there and retain
  implementation context in CONTEXT.md.

## Consumer rules

- Use the glossary's vocabulary in issues, proposals, code and tests.
- Surface conflicts with existing ADRs explicitly.
- Record a superseding decision in a new ADR referencing the old one.
- Verify owning source when documentation and implementation disagree.
