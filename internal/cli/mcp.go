package cli

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/ruaandeysel/vault/internal/db"
	mcpserver "github.com/ruaandeysel/vault/internal/mcp"
	"github.com/ruaandeysel/vault/internal/runner"
	"github.com/ruaandeysel/vault/internal/ws"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start the MCP server over stdio for AI assistant integration",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")

		database, err := db.Open(dbPath)
		if err != nil {
			return err
		}
		defer database.Close()

		hub := ws.NewHub()
		go hub.Run()

		r := runner.New(database, hub, nil)
		srv := mcpserver.New(database, r)

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		log.Println("Starting Vault MCP server (stdio)...")
		return srv.Run(ctx)
	},
}

func init() {
	mcpCmd.Flags().String("db", "/boot/config/plugins/vault/vault.db", "Database path")
	rootCmd.AddCommand(mcpCmd)
}
