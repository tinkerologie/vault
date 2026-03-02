package cli

import (
	"github.com/spf13/cobra"
)

// version is set by main via SetVersion.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "vault",
	Short: "Vault - Unraid Backup & Restore",
}

// SetVersion sets the application version (called from main before Execute).
func SetVersion(v string) {
	version = v
}

func Execute() error {
	return rootCmd.Execute()
}
