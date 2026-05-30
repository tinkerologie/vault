# Anomaly Detection & Insights

Vault watches every backup for statistical deviations from its normal behaviour and surfaces anything unusual — a backup that suddenly doubled in size, a job that started failing, or a destination that's filling up. This guide explains what it detects, how the **"Learning baseline"** period works, and how to tune it.

Anomaly detection is **on by default** and requires no configuration. It runs in the background after each backup and never slows a backup down.

---

## What it detects

Vault runs four detectors:

| Detector                | Watches                               | Example signal                                                                           |
| ----------------------- | ------------------------------------- | ---------------------------------------------------------------------------------------- |
| **Size drift**          | Backup size vs. the job's normal size | A backup that's 6× larger than usual, or shrank to under half (possible data loss)       |
| **Duration drift**      | How long a run takes vs. normal       | A run that took far longer than the job's typical time                                   |
| **Reliability**         | Run outcomes over time                | A streak of consecutive failures, or a verify check that flipped from passing to failing |
| **Capacity trajectory** | Free space on each destination        | A destination projected to run out of space soon                                         |

Size and duration use **robust statistics** (median + median-absolute-deviation, with the Iglewicz–Hoaglin modified z-score) so a single odd run doesn't skew the baseline, plus simple guard rules (e.g. "more than 5× the median" always flags).

---

## The "Learning baseline (N/10)" period

When you first create a job — or just after upgrading to a version with anomaly detection — each job shows a **`Learning baseline (N/10)`** label on the **Jobs** page.

### What it means

Before Vault can tell whether a backup is _abnormally_ large or slow, it needs to learn what _normal_ looks like for that specific job. The label shows how many finished runs it has learned from so far, out of the **10** it needs.

- **`Learning baseline (0/10)`** — brand-new job, nothing learned yet.
- **`Learning baseline (7/10)`** — 7 finished runs recorded; 3 to go.
- **Label disappears** — the job has 10 finished runs and **size & duration drift detection is now active**.

### How long does it take?

**It is run-count based, not time-based.** Vault needs **10 finished runs** of the job. Wall-clock time therefore depends entirely on the job's schedule:

| Job schedule            | Approximate time to a full baseline |
| ----------------------- | ----------------------------------- |
| Hourly                  | ~10 hours                           |
| Daily (the common case) | ~10 days                            |
| Weekly                  | ~10 weeks                           |
| Manual / "Run Now"      | As fast as you run it 10 times      |

You can speed this up by running the job manually with **Run Now** — each manual run counts toward the 10.

### What counts as a "run"?

Any run that **finishes and records a duration** counts toward the 10 — that includes successful, partial, and failed runs (the baseline also tracks the failure rate). Runs that are skipped (e.g. blocked by the circuit breaker) or never start do **not** count. Vault always learns from your **10 most recent** finished runs, so the baseline naturally tracks gradual, legitimate change over time (a slowly growing dataset won't keep alerting).

### Two thresholds, explained

- After **3** finished runs, Vault creates a provisional baseline (this is when the counter starts showing `3/10`, `4/10`, … instead of `0/10`).
- At **10** finished runs, size & duration drift detection switches on and the label clears.

### What still works _during_ learning

Not everything waits for the baseline:

- **Reliability detection is active immediately** — consecutive failures and verify regressions are flagged from the first runs, because they don't need a size/duration baseline.
- **Capacity trajectory** is per-destination (not per-job). It needs ~14 daily capacity samples before it projects an ETA.

So even a brand-new job is protected against outright failures from day one; only the _drift_ (size/duration) signals wait for the 10-run baseline.

---

## Sensitivity

Each job can be **Strict**, **Balanced** (default), or **Permissive**. This controls how far a run has to deviate before it's flagged.

| Sensitivity    | Behaviour                                            |
| -------------- | ---------------------------------------------------- |
| **Strict**     | Flags small deviations — most sensitive, more alerts |
| **Balanced**   | Moderate threshold — recommended for most users      |
| **Permissive** | Only flags large deviations — fewest alerts          |

- Set the **global default** under **Settings → General → Anomaly Detection → Default sensitivity**.
- Override it **per job** on the job's edit form (**Advanced → Anomaly sensitivity**). Leave it on **`(default)`** to follow the global setting.

Changes take effect on the next evaluation — no daemon restart needed.

---

## Where anomalies show up

| Surface             | What you see                                                                     |
| ------------------- | -------------------------------------------------------------------------------- |
| **Dashboard**       | An **Anomalies** card listing open issues (or "All clear")                       |
| **Jobs / History**  | A coloured severity badge on affected job and run rows                           |
| **/anomalies page** | The full list with filters (state, severity, scope, time) and bulk actions       |
| **Notifications**   | Unraid + Discord alerts for critical anomalies (configurable)                    |
| **MCP**             | `list_anomalies` / `get_anomaly` / `acknowledge_anomaly` tools for AI assistants |

---

## Lifecycle: resolve, acknowledge, mark-expected

- **Soft anomalies** (info / warning) **auto-resolve** when the job has a clean run again — you don't have to do anything.
- **Critical anomalies** stay until you **acknowledge** them, so a serious issue can't scroll out of view.
- **Mark as expected** — if a flagged change is intentional (e.g. you _deliberately_ added a large dataset and the backup is now legitimately bigger), use **Mark as expected**. Vault raises the baseline "floor" for that signal so the same level of growth won't keep re-alerting.

---

## Notifications

By default, only **critical** anomalies notify (to avoid noise). Configure under **Settings → General → Anomaly Detection → Notify minimum severity** (`Info`, `Warning`, or `Critical`).

- Notifications are **de-duplicated** — you won't be pinged repeatedly for the same ongoing anomaly.
- Per-job overrides: add `anomaly:critical`, `anomaly:warning`, or `anomaly:any` to a job's notify settings to force anomaly alerts for that job regardless of the global threshold.

---

## Turning it off

To disable anomaly detection entirely, toggle **Settings → General → Anomaly Detection → Anomaly detection enabled** off. Existing anomalies are kept but no new ones are raised and the background evaluator stops. Resolved/acknowledged anomalies older than 90 days are pruned automatically.

---

## FAQ

**Why does my job still say "Learning baseline" after a week?**
It needs 10 _finished_ runs, not 7 days. A weekly job takes ~10 weeks. Run it manually a few times to reach the baseline sooner.

**I marked something expected but it alerted again — why?**
"Mark as expected" raises the floor for that exact signal/fingerprint. A _different_ anomaly (e.g. a duration spike vs. the size growth you marked) is tracked separately and can still fire.

**Will detection slow my backups down?**
No. Evaluation runs asynchronously on a separate worker after the run is recorded; it never blocks the backup or the next job.
