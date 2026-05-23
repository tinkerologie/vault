// Package release exposes the bundled changelog and the latest-release
// metadata so the API can serve them to the Settings page About card.
package release

import _ "embed"

//go:embed CHANGELOG.md
var rawChangelog string

// Raw returns the bundled CHANGELOG.md content as embedded at build time.
// The Makefile / Ansible build role copies CHANGELOG.md from the repo
// root into this package directory before `go build` so the embed
// directive can find it (Go forbids parent-directory paths in
// //go:embed patterns).
func Raw() string { return rawChangelog }
