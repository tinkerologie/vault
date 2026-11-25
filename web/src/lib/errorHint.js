// Plain words for a backup item's raw Go error chain (#711). Each known cause
// gets a one-line explanation and a next step; the raw text stays available
// behind a Details disclosure. Anything unrecognised returns null so the raw
// error is shown as before, never a guess.

// Absolute paths in an error chain, in order, without the trailing colon Go
// puts between wrapped errors.
function pathsIn(raw) {
  return [...raw.matchAll(/(\/[^\s:"']+)/g)].map((match) => match[1])
}

// The path the user configured, which comes first in the chain ("backup volume
// /var/run/docker.sock: ... open /run/docker.sock: ..."): that's what to exclude.
function specialFilePath(raw) {
  const paths = pathsIn(raw)
  return paths.find((path) => /\.sock$/.test(path)) ?? paths[0]
}

const RULES = [
  {
    kind: 'special-file',
    test: /no such device or address|\.sock\b.*(?:not a regular file|unsupported file type)|is a socket|named pipe/i,
    hint(raw) {
      const path = specialFilePath(raw)
      const docker = /docker\.sock/.test(raw)
      return {
        cause: docker ? "The Docker socket can't be backed up." : "A socket or special file can't be backed up.",
        action: path
          ? `Exclude ${path} from this job's item, then run the backup again.`
          : "Exclude the socket or special file from this job's item, then run the backup again.",
        ...(path ? { excludePath: path } : {}),
      }
    },
  },
  {
    kind: 'permission',
    test: /permission denied|operation not permitted/i,
    hint: () => ({
      cause: "Vault wasn't allowed to read this item.",
      action: 'Check the file permissions or exclude the protected path, then run the backup again.',
    }),
  },
  {
    kind: 'no-space',
    test: /no space left on device|disk quota exceeded/i,
    hint: () => ({
      cause: 'The destination or staging area ran out of space.',
      action: 'Free up space or lower retention on the destination, then run the backup again.',
    }),
  },
  {
    kind: 'unreachable',
    test: /connection refused|no route to host|host is unreachable|network is unreachable|i\/o timeout|connection timed out|connection reset/i,
    hint: () => ({
      cause: "Vault couldn't reach the destination.",
      action: 'Check that the storage server is online and reachable, then test it on the Storage page.',
    }),
  },
  {
    kind: 'missing',
    test: /no such file or directory|\bnot found\b|does not exist/i,
    hint: () => ({
      cause: 'The item no longer exists where the job expects it.',
      action: "Check whether it was moved or removed, and update the job's items.",
    }),
  },
]

/**
 * @param {string | null | undefined} raw The item's raw error text.
 * @returns {{ kind: string, cause: string, action: string, excludePath?: string } | null}
 */
export function errorHint(raw) {
  const text = String(raw ?? '')
  if (!text.trim()) return null
  const rule = RULES.find((candidate) => candidate.test.test(text))
  return rule ? { kind: rule.kind, ...rule.hint(text) } : null
}
