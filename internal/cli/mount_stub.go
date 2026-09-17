//go:build !linux

package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

var mountCmd = &cobra.Command{
	Use:   "mount [flags]",
	Short: "Mount a deduplicated backup restore point as a read-only FUSE filesystem",
	RunE: func(cmd *cobra.Command, args []string) error {
		return errors.New("vault mount is only supported on Linux")
	},
}

func init() {
	rootCmd.AddCommand(mountCmd)
}
