import type { VaultMockOptions } from '../mocks/vault-api';

/** Every item the mock's discovery endpoints report, as health-summary keys. */
export const ALL_ITEM_KEYS = [
  'container:plex', 'container:nextcloud', 'container:postgres',
  'vm:home-assistant', 'vm:ubuntu-vm',
  'folder:Flash Drive', 'folder:Appdata',
];

export const run = (id: number, status: string, jobId = 1) => ({
  id,
  job_id: jobId,
  job_name: jobId === 1 ? 'Docker & Appdata' : 'VM Backups',
  status,
  backup_type: 'full',
  started_at: `2026-10-0${(id % 9) + 1}T02:00:00Z`,
  completed_at: `2026-10-0${(id % 9) + 1}T02:15:00Z`,
  bytes_written: 1024,
  duration_seconds: 900,
});

/** A summary as the daemon would send it. Items not listed count as unprotected. */
export const summary = (protectedKeys: string[], extra: Record<string, unknown> = {}) => ({
  health_score: 100,
  total_items: protectedKeys.length,
  protected_items: protectedKeys.length,
  protected_keys: protectedKeys,
  pending_keys: [],
  protection_pct: 100,
  success_rate: 100,
  recent_success: 5,
  recent_failed: 0,
  last_success_at: '2026-10-06T02:15:00Z',
  ...extra,
});

export const HEALTHY: VaultMockOptions = { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'completed')] };

const FRESH_SUMMARY = summary([], {
  health_score: 0, recent_success: 0, last_success_at: null, pending_keys: ALL_ITEM_KEYS, total_items: 7, protection_pct: 0, success_rate: 0,
});

/** One mock configuration per banner state that needs no route overrides. */
export const BANNER_STATES: { name: string; options: VaultMockOptions; title: string }[] = [
  { name: 'healthy', options: HEALTHY, title: 'Backups healthy' },
  { name: 'unprotected', options: { healthSummary: summary(ALL_ITEM_KEYS.slice(0, 4)), historyRuns: [run(1, 'completed')] }, title: 'Needs attention' },
  { name: 'failures', options: { healthSummary: summary(ALL_ITEM_KEYS, { recent_failed: 1 }), historyRuns: [run(1, 'failed')] }, title: 'Needs attention' },
  { name: 'warnings', options: { healthSummary: summary(ALL_ITEM_KEYS), historyRuns: [run(1, 'partial')] }, title: 'Needs attention' },
  { name: 'first run, no storage', options: { jobs: [], storage: [], healthSummary: FRESH_SUMMARY, historyRuns: [] }, title: 'Set up your first backup' },
  { name: 'no jobs', options: { jobs: [], healthSummary: FRESH_SUMMARY, historyRuns: [] }, title: 'Create your first backup job' },
  { name: 'waiting for first backup', options: { healthSummary: FRESH_SUMMARY, historyRuns: [] }, title: 'No backups completed yet' },
  { name: 'replica healthy', options: { replica: true, ...HEALTHY }, title: 'Backups healthy' },
  { name: 'replica with a failure', options: { replica: true, healthSummary: summary(ALL_ITEM_KEYS, { recent_failed: 1 }), historyRuns: [run(1, 'failed')] }, title: 'Needs attention' },
];
