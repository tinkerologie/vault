#!/bin/sh
# Build the plugin package archive from a staged tree with every entry owned
# by root:root, then verify it with check-package-ownership.sh (#514).
# Usage: make-package-archive.sh <staging-dir> <output.txz>
set -eu
if [ $# -ne 2 ]; then
	echo "usage: make-package-archive.sh <staging-dir> <output.txz>" >&2
	exit 2
fi
src="$1"
out="$2"
case "$out" in /*) ;; *) out="$(pwd)/$out" ;; esac
here="$(cd "$(dirname "$0")" && pwd)"
(cd "$src" && COPYFILE_DISABLE=1 "${TAR:-tar}" --owner=0 --group=0 --numeric-owner -cJf "$out" usr/ etc/)
sh "$here/check-package-ownership.sh" "$out"
