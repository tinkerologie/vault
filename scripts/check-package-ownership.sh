#!/bin/sh
# Fail unless every entry of a plugin package is owned by root:root (#514).
# installpkg runs as root and applies the archive's owners, so a package built
# by the CI runner (UID 1001) used to hand /usr, /usr/local/emhttp and /etc to
# that UID on install. Works with GNU tar ("0/0") and bsdtar ("0 0") listings.
set -u
pkg="${1:?usage: check-package-ownership.sh <package.txz>}"
if ! listing="$(tar --numeric-owner -tvJf "$pkg")"; then
	echo "ERROR: cannot list $pkg" >&2
	exit 1
fi
bad="$(printf '%s\n' "$listing" | awk '{ split($2, o, "/"); u = (o[2] != "") ? o[1] : $3; g = (o[2] != "") ? o[2] : $4; if (u != "0" || g != "0") print }')"
if [ -n "$bad" ]; then
	echo "ERROR: package entries not owned by root:root:" >&2
	printf '%s\n' "$bad" >&2
	exit 1
fi
