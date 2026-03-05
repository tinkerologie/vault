# Frontend Linting & UX Polish Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Set up ESLint with Svelte plugin, fix all lint/a11y issues, then do a comprehensive UX review with Playwright and fix any visual issues found.

**Architecture:** Add ESLint + eslint-plugin-svelte to `web/`, integrate into Makefile and package.json scripts. Fix all reported issues. Deploy to Unraid and use Playwright to screenshot every page for visual QA.

**Tech Stack:** ESLint 9+, eslint-plugin-svelte, @eslint/js, globals, Playwright (via playwright-cli skill)

---

### Task 1: Install ESLint Dependencies

**Files:**

- Modify: `web/package.json`

**Step 1: Install packages**

Run:

```bash
cd web && npm install -D eslint @eslint/js eslint-plugin-svelte globals
```

**Step 2: Verify installation**

Run:

```bash
cd web && npx eslint --version
```

Expected: ESLint version 9.x+

**Step 3: Commit**

```bash
git add web/package.json web/package-lock.json
git commit -m "chore: add eslint with svelte plugin dependencies"
```

---

### Task 2: Create ESLint Config

**Files:**

- Create: `web/eslint.config.js`

**Step 1: Create config file**

```js
import js from "@eslint/js";
import svelte from "eslint-plugin-svelte";
import globals from "globals";

export default [
  js.configs.recommended,
  ...svelte.configs.recommended,
  {
    languageOptions: {
      globals: {
        ...globals.browser,
      },
    },
  },
  {
    ignores: ["dist/"],
  },
];
```

**Step 2: Run ESLint to see baseline issues**

Run:

```bash
cd web && npx eslint src/
```

Record all issues — these will be fixed in subsequent tasks.

**Step 3: Commit**

```bash
git add web/eslint.config.js
git commit -m "chore: add eslint flat config for svelte project"
```

---

### Task 3: Add Lint Scripts to Package.json and Makefile

**Files:**

- Modify: `web/package.json` (add `"lint"` script)
- Modify: `Makefile` (add `lint-web` target, update `lint` target)

**Step 1: Add npm lint script**

In `web/package.json`, add to `"scripts"`:

```json
"lint": "eslint src/"
```

**Step 2: Add Makefile target**

Add after the existing `lint` target in `Makefile`:

```makefile
lint-web:
 cd web && npm run lint
```

**Step 3: Verify**

Run:

```bash
make lint-web
```

Expected: ESLint runs and shows any issues.

**Step 4: Commit**

```bash
git add web/package.json Makefile
git commit -m "chore: add lint-web target and npm lint script"
```

---

### Task 4: Fix All ESLint Issues

**Files:** Multiple `.svelte` and `.js` files — exact files depend on ESLint output from Task 2.

**Known Svelte a11y warnings to fix:**

**4a. Labels without associated controls (6 instances)**

These labels describe button groups or custom components, not native inputs. Fix by changing `<label>` to `<span>` since these are visual labels, not form labels:

- `web/src/components/ScheduleBuilder.svelte:160` — "Frequency" label → `<span>`
- `web/src/components/ScheduleBuilder.svelte:187` — "Days" label → `<span>`
- `web/src/components/BackupModeSelector.svelte:53` — "Container Backup Mode" → `<span>`
- `web/src/components/BackupModeSelector.svelte:89` — "VM Backup Mode" → `<span>`
- `web/src/pages/Settings.svelte:777` — "Custom Path (optional)" → add `id` to PathBrowser input and `for` attribute, or use `<span>`
- `web/src/pages/Settings.svelte:1073` — "Custom Snapshot Path (optional)" → same as above
- `web/src/pages/Replication.svelte:412` — "Sync Schedule" → `<span>`

For each, replace:

```svelte
<label class="...">Text</label>
```

With:

```svelte
<span class="...">Text</span>
```

**4b. Click events without key events (2 instances)**

Add `role="button"` and `onkeydown` handlers to clickable divs:

- `web/src/components/ComplianceBadge.svelte:52` — Add `role="button" tabindex="0" onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); expanded = !expanded } }}` and remove the `svelte-ignore` comment.

- `web/src/pages/Recovery.svelte:138` — Add `role="button" tabindex="0" onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleStep(step.step) } }}` and remove the `svelte-ignore` comment.

**4c. Autofocus (1 instance)**

- `web/src/pages/Jobs.svelte:395` — The autofocus on the inline name-edit input is intentional UX (user clicks to edit, input appears focused). Add `<!-- svelte-ignore a11y_autofocus -->` above the input since this is a deliberate interaction pattern.

**Step 1: Fix all the issues listed above**

Apply the changes to each file.

**Step 2: Run ESLint to verify zero issues**

Run:

```bash
cd web && npx eslint src/
```

Expected: 0 errors, 0 warnings.

**Step 3: Run Vite build to verify zero warnings**

Run:

```bash
cd web && npm run build 2>&1 | grep "vite-plugin-svelte"
```

Expected: No output (no warnings).

**Step 4: Commit**

```bash
git add -A web/src/
git commit -m "fix: resolve all eslint and a11y warnings in frontend"
```

---

### Task 5: Deploy to Unraid

**Step 1: Build and deploy**

Run:

```bash
make redeploy
```

Expected: Build succeeds, deploys to Unraid, verification passes.

---

### Task 6: Playwright UX Review — All Pages

Use the `playwright-cli` skill to navigate every page on `http://192.168.20.21:24085` and take screenshots.

**Pages to review (9 total):**

| #   | Route       | URL                                        |
| --- | ----------- | ------------------------------------------ |
| 1   | Dashboard   | `http://192.168.20.21:24085/#/`            |
| 2   | Jobs        | `http://192.168.20.21:24085/#/jobs`        |
| 3   | Storage     | `http://192.168.20.21:24085/#/storage`     |
| 4   | History     | `http://192.168.20.21:24085/#/history`     |
| 5   | Restore     | `http://192.168.20.21:24085/#/restore`     |
| 6   | Logs        | `http://192.168.20.21:24085/#/logs`        |
| 7   | Replication | `http://192.168.20.21:24085/#/replication` |
| 8   | Recovery    | `http://192.168.20.21:24085/#/recovery`    |
| 9   | Settings    | `http://192.168.20.21:24085/#/settings`    |

**For each page, evaluate:**

- Layout & alignment — consistent spacing, proper grid alignment, no overflow
- Typography — clear hierarchy, readable sizes, consistent weights
- Color & contrast — readable text, consistent color usage
- Components — buttons, cards, modals polished and consistent
- Empty states — graceful when no data
- Navigation — sidebar active state clear, professional look

Take a screenshot of each page and note any issues.

---

### Task 7: Fix UX Issues Found

**Files:** TBD based on Playwright review findings from Task 6.

Fix any visual/UX issues identified during the review. Common fixes may include:

- Spacing inconsistencies (padding/margin adjustments)
- Typography hierarchy issues
- Color/contrast problems
- Alignment issues
- Empty state improvements
- Component styling polish

**Step 1: Fix each issue**

Apply CSS/Svelte changes for each identified issue.

**Step 2: Build and deploy**

Run:

```bash
make redeploy
```

**Step 3: Re-verify with Playwright**

Use `playwright-cli` to re-screenshot affected pages and confirm fixes.

**Step 4: Commit**

```bash
git add -A web/src/
git commit -m "fix: UX polish improvements from visual review"
```

---

### Task 8: Final Verification

**Step 1: Run all linters**

```bash
make lint && make lint-web
```

Expected: 0 issues on both.

**Step 2: Run Go tests**

```bash
make test-short
```

Expected: All pass.

**Step 3: Run Vite build**

```bash
cd web && npm run build
```

Expected: Clean build, no warnings.

**Step 4: Final Playwright pass**

Use `playwright-cli` to screenshot every page one final time confirming everything looks polished.
