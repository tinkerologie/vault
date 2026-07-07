package unraid

import "os"

// recycleBinPlgPath is the installer file created by the Unraid Recycle Bin
// plugin. It is a variable so tests can override it.
var recycleBinPlgPath = "/boot/config/plugins/recycle.bin.plg"

// RecycleBinDir is the per-share/flash folder the Recycle Bin plugin uses to
// hold Samba-deleted files. Excluding it keeps trash out of folder and flash
// backups (issue #204).
const RecycleBinDir = ".Recycle.Bin"

// RecycleBinInstalled reports whether the Unraid Recycle Bin plugin is
// installed, detected by the presence of its .plg installer file under
// /boot/config/plugins/. Returns false on non-Unraid hosts.
func RecycleBinInstalled() bool {
	_, err := os.Stat(recycleBinPlgPath)
	return err == nil
}
