// Client-side mirror of the server's local storage root policy
// (safepath.ValidateLocalStorageRoot, issue #514). The server stays the
// authority and also resolves symlinks; this only gives early feedback in
// the storage form and the directory picker.

const PROTECTED_TREES = [
  '/bin', '/boot', '/dev', '/etc', '/lib', '/lib32', '/lib64', '/libx32',
  '/proc', '/root', '/run', '/sbin', '/sys', '/usr',
  '/var/cache', '/var/lib', '/var/local', '/var/log', '/var/run', '/var/spool',
]

const SHARED_PARENTS = [
  '/home', '/media', '/mnt', '/mnt/addons', '/mnt/disks', '/mnt/remotes',
  '/mnt/rootshare', '/mnt/user', '/mnt/user0', '/opt', '/srv', '/tmp',
]

/** Lexically cleans an absolute POSIX path ("/a//b/../c/" -> "/a/c"). */
export function cleanAbsolutePath(p) {
  const out = []
  for (const seg of p.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') out.pop()
    else out.push(seg)
  }
  return '/' + out.join('/')
}

function within(parent, child) {
  return parent === '/' ? child !== '/' : child.startsWith(parent + '/')
}

/**
 * Returns a human-readable problem with a local destination path, or '' when
 * the path looks acceptable.
 * @param {string} raw
 */
export function localStoragePathProblem(raw) {
  const trimmed = (raw ?? '').trim()
  if (!trimmed) return 'Choose a directory for this destination.'
  if (!trimmed.startsWith('/')) return 'The path must be absolute, for example /mnt/user/backups/vault.'
  const p = cleanAbsolutePath(trimmed)
  for (const tree of PROTECTED_TREES) {
    if (p === tree || within(tree, p) || within(p, tree)) {
      return `${p} is a protected system location. Choose a dedicated backup directory such as /mnt/user/backups/vault.`
    }
  }
  if (SHARED_PARENTS.includes(p)) {
    return `${p} is a shared parent directory. Choose a dedicated subdirectory such as ${p}/vault-backups.`
  }
  return ''
}
