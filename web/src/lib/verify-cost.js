// Per-backup verification ("Verify backups after writing") reads every backup
// back from its destination. On a remote destination that can mean a large,
// slow, and sometimes billed download after each run (#457). These helpers
// decide when the job form discloses that cost.

/**
 * Returns the cost disclosure to show for the selected destination, or null
 * when none applies. Matches the job form's local/remote rule: no destination
 * or a `local` one is local; every other type (smb, nfs, sftp, webdav, s3) is
 * remote.
 *
 * @param {Array<{id: number, type: string}>} storageList
 * @param {number | null | undefined} destId
 * @param {boolean} verifyBackup
 * @returns {{ providerCharges: boolean } | null}
 */
export function verifyCostWarning(storageList, destId, verifyBackup) {
  if (!verifyBackup) return null
  const dest = (storageList || []).find(s => s.id === destId)
  if (!dest || dest.type === 'local') return null
  // S3 and S3-compatible providers bill for requests, retrieval, or egress.
  return { providerCharges: dest.type === 's3' }
}
