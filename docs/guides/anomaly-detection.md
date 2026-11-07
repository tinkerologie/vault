# Anomaly Detection & Insights

Vault examines each backup for changes in size, duration, and other data. It also detects repeated failures and estimates when a storage destination will run out of space.

Anomaly detection is on by default. Vault examines the data after each backup.

---

## What it detects

Vault runs four detectors:

| Detector                | Watches                               | Example signal                                                                           |
| ----------------------- | ------------------------------------- | ---------------------------------------------------------------------------------------- |
| **Size drift**          | Backup size vs. the job's normal size | A backup that's 6× larger than usual, or shrank to under half (possible data loss)       |
| **Duration drift**      | How long a run takes vs. normal       | A run that took far longer than the job's typical time                                   |
| **Reliability**         | Run outcomes over time                | A streak of consecutive failures, or a verify check that flipped from passing to failing |
| **Capacity trajectory** | Free space on each destination        | A destination projected to run out of space soon                                         |

Vault uses the median, median absolute deviation, and Iglewicz–Hoaglin modified z-score for size and duration. One unusual run has little effect on the baseline. Vault also uses fixed rules. For example, a value more than five times the median can cause an anomaly.

---

## The "Learning baseline (N/10)" period

The **Jobs** page shows **`Learning baseline (N/10)`** for a new job. An existing job can also show this label after an upgrade.

### What it means

Vault uses data from finished runs to identify changes in backup size or duration. The label shows the number of finished runs out of ten.

- **`Learning baseline (0/10)`** — brand-new job, nothing learned yet.
- **`Learning baseline (7/10)`** — Vault has data for seven runs. Three more runs are necessary.
- **Label disappears** — the job has 10 finished runs and **size & duration drift detection is now active**.

### How long does it take?

Ten finished runs are necessary. The time to collect those runs depends on the job schedule.

| Job schedule            | Approximate time to a full baseline |
| ----------------------- | ----------------------------------- |
| Hourly                  | ~10 hours                           |
| Daily (the common case) | ~10 days                            |
| Weekly                  | ~10 weeks                           |
| Manual / "Run Now"      | As fast as you run it 10 times      |

You can speed this up by running the job manually with **Run Now** — each manual run counts toward the 10.

### What counts as a "run"?

Vault counts successful, partial, and failed runs if they have a duration. Vault does not count runs that it skips or does not start. The baseline uses the ten most recent finished runs. The baseline changes as the data changes.

### Two thresholds, explained

- After **3** finished runs, Vault creates a provisional baseline (this is when the counter starts showing `3/10`, `4/10`, … instead of `0/10`).
- At **10** finished runs, size & duration drift detection switches on and the label clears.

### What still works _during_ learning

Not everything waits for the baseline:

- **Reliability detection.** Vault can report consecutive failures or a change from successful to failed verification before it has a baseline.
- **Capacity trajectory** is per-destination (not per-job). Vault uses approximately 14 daily capacity samples to estimate when the destination will run out of space.

Ten finished runs are necessary to detect changes in size and duration.

---

## Sensitivity

Each job can be **Strict**, **Balanced** (default), or **Permissive**. This controls how far a run has to deviate before it's flagged.

| Sensitivity    | Behavior                                             |
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

Each anomaly message describes the difference between the result and the expected result.

- **Duration:** _This backup took 4m 23s, about 1.2× its usual 3m 40s._
- **Size (growth):** _This backup grew to 20 GB, about 5× its usual 4 GB._
- **Size (shrinkage):** _This backup shrank to 1.8 GB, only 45% of its usual 4 GB — this can signal missing or corrupted data._
- **Reliability:** _This backup has failed 3 runs in a row and needs attention._
- **Capacity:** _Storage "backups" uses 12 GB more each day. At this rate, it will run out of space in approximately 6 days._

Notifications show the same sentence plus a short line of context (e.g. _Based on the last 10 samples_) instead of a raw JSON blob. The precise statistics (z-score, growth factor, window size) are still available in the structured `details` field via the API, MCP, and the web UI's **Raw details** expander.

---

## Lifecycle: resolve, acknowledge, mark-expected

- Vault closes an info or warning anomaly when the job has a run without that anomaly.
- Vault keeps a critical anomaly open until you acknowledge it.
- If you expect a change, select **Mark as expected**. Vault adjusts the threshold for that signal so the same change does not cause another alert.

---

## Notifications

By default, only **critical** anomalies notify (to avoid noise). Configure under **Settings → General → Anomaly Detection → Notify minimum severity** (`Info`, `Warning`, or `Critical`).

- Vault suppresses repeat notifications for the same open anomaly.
- Per-job overrides: add `anomaly:critical`, `anomaly:warning`, or `anomaly:any` to a job's notify settings to force anomaly alerts for that job regardless of the global threshold.

---

## Turning it off

To disable anomaly detection entirely, toggle **Settings → General → Anomaly Detection → Anomaly detection enabled** off. Existing anomalies are kept but no new ones are raised and the background evaluator stops. Resolved/acknowledged anomalies older than 90 days are pruned automatically.

---

## FAQ

**Why does my job still say "Learning baseline" after a week?**
Ten finished runs are necessary. A weekly job takes ~10 weeks. Run it manually a few times to reach the baseline sooner.

**I marked something expected but it alerted again — why?**
"Mark as expected" raises the floor for that exact signal/fingerprint. A _different_ anomaly (e.g. a duration spike vs. the size growth you marked) is tracked separately and can still fire.

**Will detection slow my backups down?**
No. Vault examines the run on a separate worker after it records the result. This does not block the backup or the next job.
