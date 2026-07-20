-- Production-scale sanitized UI fixture for a fresh, disposable local Vault DB.
-- Run only against a database created specifically for QA.

.bail on
PRAGMA foreign_keys = ON;
BEGIN IMMEDIATE;

INSERT INTO storage_destinations
  (name, type, config, dedup_enabled, last_health_check_at,
   last_health_check_status, capacity_total_bytes, capacity_used_bytes,
   capacity_free_bytes, capacity_probed_at, capacity_source,
   anomaly_sensitivity)
VALUES
  ('QA Local 01', 'local', '{"path":"/tmp/vault-qa-data/storage-01"}', 0, datetime('now'), 'healthy', 10995116277760, 5497558138880, 5497558138880, datetime('now'), 'statfs', ''),
  ('QA Local 02', 'local', '{"path":"/tmp/vault-qa-data/storage-02"}', 1, datetime('now'), 'healthy', 21990232555520, 16492674416640, 5497558138880, datetime('now'), 'statfs', 'balanced'),
  ('QA Local 03', 'local', '{"path":"/tmp/vault-qa-data/storage-03"}', 0, datetime('now'), 'warning', 10995116277760, 9895604649984, 1099511627776, datetime('now'), 'statfs', 'strict'),
  ('QA Local 04', 'local', '{"path":"/tmp/vault-qa-data/storage-04"}', 1, datetime('now'), 'healthy', 43980465111040, 10995116277760, 32985348833280, datetime('now'), 'statfs', ''),
  ('QA Local 05', 'local', '{"path":"/tmp/vault-qa-data/storage-05"}', 0, NULL, '', NULL, NULL, NULL, NULL, '', ''),
  ('QA Local 06', 'local', '{"path":"/tmp/vault-qa-data/storage-06"}', 1, datetime('now'), 'error', 10995116277760, 10775213952205, 219902325555, datetime('now'), 'statfs', 'permissive'),
  ('QA SFTP', 'sftp', '{"host":"sftp.qa.invalid","port":22,"user":"qa","password":"","base_path":"vault"}', 0, NULL, '', NULL, NULL, NULL, NULL, '', ''),
  ('QA SMB', 'smb', '{"host":"smb.qa.invalid","share":"backups","user":"qa","password":"","base_path":"vault"}', 0, NULL, '', NULL, NULL, NULL, NULL, '', ''),
  ('QA NFS', 'nfs', '{"host":"nfs.qa.invalid","export":"/exports/qa","base_path":"vault","version":"4"}', 0, NULL, '', NULL, NULL, NULL, NULL, '', ''),
  ('QA WebDAV', 'webdav', '{"url":"https://webdav.qa.invalid","username":"qa","password":"","base_path":"vault"}', 0, NULL, '', NULL, NULL, NULL, NULL, '', ''),
  ('QA S3', 's3', '{"bucket":"qa-vault","region":"us-east-1","endpoint":"https://s3.qa.invalid","base_path":"vault","force_path_style":true}', 0, NULL, '', NULL, NULL, NULL, NULL, '', '');

WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x + 1 FROM n WHERE x < 120)
INSERT INTO jobs
  (name, description, enabled, schedule, backup_type_chain, retention_count,
   retention_days, compression, compression_level, encryption, container_mode,
   vm_mode, notify_on, verify_backup, storage_dest_id, keep_latest, keep_daily,
   keep_weekly, keep_monthly, keep_yearly, verify_schedule, verify_mode,
   anomaly_sensitivity, max_parallel_uploads, adaptive_enabled, created_at,
   updated_at)
SELECT
  printf('QA Job %03d', x),
  CASE WHEN x = 120
    THEN 'Unicode and long description: café 日本語 — sanitized QA data with a deliberately verbose summary for overflow checks.'
    ELSE printf('Sanitized production-scale QA job %03d', x)
  END,
  CASE WHEN x % 4 = 0 THEN 0 ELSE 1 END,
  CASE WHEN x % 3 = 0 THEN printf('%d %d * * *', x % 60, x % 24) ELSE '' END,
  CASE x % 3 WHEN 0 THEN 'full' WHEN 1 THEN 'incremental' ELSE 'differential' END,
  14, 90,
  CASE x % 3 WHEN 0 THEN 'zstd' WHEN 1 THEN 'gzip' ELSE 'none' END,
  CASE WHEN x % 3 = 2 THEN '' ELSE 'better' END,
  'none',
  CASE WHEN x % 2 = 0 THEN 'one_by_one' ELSE 'all_at_once' END,
  CASE WHEN x % 2 = 0 THEN 'snapshot' ELSE 'cold' END,
  CASE x % 3 WHEN 0 THEN 'always' WHEN 1 THEN 'failure' ELSE 'never' END,
  1, ((x - 1) % 11) + 1,
  7, 7, 4, 12, 3,
  CASE WHEN x % 10 = 0 THEN '0 3 * * 0' ELSE '' END,
  CASE WHEN x % 2 = 0 THEN 'quick' ELSE 'deep' END,
  CASE x % 4 WHEN 0 THEN '' WHEN 1 THEN 'strict' WHEN 2 THEN 'balanced' ELSE 'permissive' END,
  1 + (x % 8), CASE WHEN x % 5 = 0 THEN 1 ELSE 0 END,
  datetime('now', '-' || (121 - x) || ' days'), datetime('now')
FROM n;

WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x + 1 FROM n WHERE x < 120)
INSERT INTO job_items(job_id, item_type, item_name, item_id, settings, sort_order)
SELECT x, 'container', printf('qa-container-%03d', x), printf('container-%03d', x), '{"backup_mode":"full"}', 0 FROM n
UNION ALL
SELECT x, 'vm', printf('qa-vm-%03d', x), printf('vm-%03d', x), '{"backup_mode":"snapshot"}', 1 FROM n
UNION ALL
SELECT x, 'folder', printf('qa-folder-%03d', x), printf('folder-%03d', x), printf('{"path":"/tmp/vault-qa-data/source-%03d"}', x), 2 FROM n;

WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x + 1 FROM n WHERE x < 12000)
INSERT INTO job_runs
  (job_id, status, backup_type, started_at, completed_at, log, items_total,
   items_done, items_failed, size_bytes, run_type, retry_attempt)
SELECT
  ((x - 1) % 120) + 1,
  CASE WHEN x % 37 = 0 THEN 'running' WHEN x % 29 = 0 THEN 'failed' WHEN x % 31 = 0 THEN 'skipped' ELSE 'completed' END,
  CASE x % 3 WHEN 0 THEN 'full' WHEN 1 THEN 'incremental' ELSE 'differential' END,
  datetime('now', '-' || (12000 - x + 61) || ' minutes'),
	CASE WHEN x % 37 = 0 THEN NULL
	  ELSE datetime('now', '-' || (12000 - x + 61) || ' minutes', '+' || (20 + (x % 3600)) || ' seconds')
  END,
  CASE WHEN x % 29 = 0
    THEN '[{"level":"error","message":"Sanitized simulated failure","item":"qa-fixture"}]'
    ELSE '[{"level":"info","message":"Sanitized run completed","item":"qa-fixture"}]'
  END,
  3, CASE WHEN x % 37 = 0 THEN 1 WHEN x % 29 = 0 THEN 2 ELSE 3 END,
	CASE WHEN x % 37 = 0 THEN 0 WHEN x % 29 = 0 THEN 1 ELSE 0 END,
  1048576 * (100 + (x % 50000)),
  CASE WHEN x % 17 = 0 THEN 'restore' ELSE 'backup' END, 0
FROM n;

INSERT INTO restore_points
  (job_run_id, job_id, backup_type, storage_path, metadata, size_bytes,
   parent_restore_point_id, created_at)
SELECT
  id, job_id, backup_type, printf('qa/job-%03d/run-%06d', job_id, id),
  printf('{"items":[{"name":"qa-folder-%03d","type":"folder","size_bytes":%d}]}', job_id, size_bytes),
  size_bytes, NULL, completed_at
FROM job_runs
WHERE status = 'completed' AND completed_at IS NOT NULL AND id % 5 = 0;

INSERT INTO verify_runs
  (restore_point_id, mode, status, files_checked, files_failed, bytes_read,
   started_at, completed_at, error_summary)
SELECT
  id, CASE WHEN id % 2 = 0 THEN 'quick' ELSE 'deep' END,
  CASE WHEN id % 19 = 0 THEN 'failed' ELSE 'passed' END,
  3, CASE WHEN id % 19 = 0 THEN 1 ELSE 0 END, size_bytes,
  datetime(created_at, '+5 minutes'), datetime(created_at, '+6 minutes'),
  CASE WHEN id % 19 = 0 THEN 'Sanitized checksum mismatch' ELSE '' END
FROM restore_points
WHERE id % 3 = 0;

INSERT INTO job_baselines
  (job_id, sample_count, bytes_median, bytes_mad, duration_median, duration_mad,
   failure_rate, updated_at)
SELECT id, 100, 1048576 * (1000 + (id % 500)), 1048576 * 50,
       300 + (id % 120), 30, 0.034, datetime('now')
FROM jobs;

WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x + 1 FROM n WHERE x < 240)
INSERT INTO anomalies
  (fingerprint, detector, severity, scope_kind, scope_id, metric, observed,
   expected, deviation, job_run_id, summary, details, state, first_seen_at,
   last_seen_at)
SELECT
  printf('qa-anomaly-%04d', x),
  CASE WHEN x % 2 = 0 THEN 'size_drift' ELSE 'duration_drift' END,
  CASE x % 3 WHEN 0 THEN 'critical' WHEN 1 THEN 'warning' ELSE 'info' END,
  CASE WHEN x % 5 = 0 THEN 'destination' ELSE 'job' END,
  CASE WHEN x % 5 = 0 THEN ((x - 1) % 11) + 1 ELSE ((x - 1) % 120) + 1 END,
  CASE WHEN x % 2 = 0 THEN 'size_bytes' ELSE 'duration_seconds' END,
  1000 + x * 10, 1000, CAST(x AS REAL) / 100, ((x - 1) % 12000) + 1,
  CASE WHEN x % 2 = 0
    THEN printf('This backup grew to %.1f GB, about %.1f× its usual 1 GB.', 1.0 + x / 100.0, 1.0 + x / 100.0)
    ELSE printf('This backup took %d seconds, above its usual 300 seconds.', 300 + x)
  END,
  printf('{"observed":%d,"expected":1000,"fixture":"sanitized"}', 1000 + x * 10),
  CASE x % 4 WHEN 0 THEN 'open' WHEN 1 THEN 'acknowledged' WHEN 2 THEN 'resolved' ELSE 'expected' END,
  datetime('now', '-' || (x % 90) || ' days'),
  datetime('now', '-' || (x % 30) || ' days')
FROM n;

WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x + 1 FROM n WHERE x < 1000)
INSERT INTO activity_log(level, category, message, details, created_at)
SELECT
  CASE n.x % 3 WHEN 0 THEN 'error' WHEN 1 THEN 'warning' ELSE 'info' END,
  CASE n.x % 4 WHEN 0 THEN 'backup' WHEN 1 THEN 'restore' WHEN 2 THEN 'health' ELSE 'system' END,
  printf('Sanitized QA activity event %04d', n.x),
  printf('{"duration_seconds":%d,"size_bytes":%d,"fixture":"sanitized"}', n.x % 7200, 1048576 * (n.x % 5000)),
  datetime('now', '-' || (1000 - n.x) || ' minutes')
FROM n;

WITH RECURSIVE s(y) AS (VALUES (1) UNION ALL SELECT y + 1 FROM s WHERE y < 90)
INSERT INTO destination_capacity_samples(dest_id, sampled_at, free_bytes, total_bytes)
SELECT d.id, datetime('now', '-' || (90 - s.y) || ' days'),
       d.capacity_total_bytes - (s.y * 10737418240) - (d.id * 104857600),
       d.capacity_total_bytes
FROM storage_destinations d CROSS JOIN s
WHERE d.capacity_total_bytes IS NOT NULL;

WITH RECURSIVE n(x) AS (VALUES (1) UNION ALL SELECT x + 1 FROM n WHERE x < 8)
INSERT INTO replication_sources
  (name, url, storage_dest_id, schedule, enabled, last_sync_at,
   last_sync_status, last_sync_error, type, config)
SELECT
  printf('QA Replica %02d', x), printf('https://replica-%02d.qa.invalid', x),
  ((x - 1) % 11) + 1, '15 2 * * *', CASE WHEN x % 4 = 0 THEN 0 ELSE 1 END,
  datetime('now', '-' || x || ' hours'),
  CASE WHEN x % 5 = 0 THEN 'failed' ELSE 'success' END,
  CASE WHEN x % 5 = 0 THEN 'Sanitized timeout' ELSE '' END,
  'remote_vault', '{"api_key":"","sanitized":true}'
FROM n;

INSERT INTO settings(key, value) VALUES
  ('history_retention_days', '365'),
  ('anomaly_detection_enabled', 'true'),
  ('replication_enabled', 'true'),
  ('notifications_enabled', 'false'),
  ('anomaly_sensitivity_default', 'balanced'),
  ('anomaly_notify_min_severity', 'warning'),
  ('retry_max_default', '3'),
  ('retry_delays_default', '[900,3600,14400]'),
  ('auto_throttle_enabled', 'true'),
  ('auto_throttle_link_mbps', '1000'),
  ('auto_throttle_floor_mbps', '25'),
  ('dedup_compaction_min_dead_ratio', '0.5'),
  ('container_backup_enabled', 'true'),
  ('vm_backup_enabled', 'true'),
  ('folder_backup_enabled', 'true'),
  ('flash_backup_enabled', 'true'),
  ('backup_rule_enabled', 'true'),
  ('storage_verbose_logging', 'false')
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

COMMIT;
