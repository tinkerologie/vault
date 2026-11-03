//go:build !unix

package db

import "os"

// ownedByProcess has no portable owner to compare on this platform; Vault
// itself only runs on Linux.
func ownedByProcess(os.FileInfo) bool { return true }
