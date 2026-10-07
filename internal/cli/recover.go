package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ruaan-deysel/vault/internal/format"
	"github.com/ruaan-deysel/vault/internal/recovery"
)

// recoverReportName is the pattern of the report written into the extraction
// folder after every run; each run gets its own file so a later failed
// attempt never replaces an earlier report.
const recoverReportName = "vault-recover-report-<time>.json"

var (
	recoverType           string
	recoverPath           string
	recoverConfigFile     string
	recoverKeyFile        string
	recoverPassphraseFile string
	recoverJSON           bool
	recoverVerbose        bool

	recoverListJob string

	recoverPoint     string
	recoverItems     []string
	recoverIncludes  []string
	recoverTo        string
	recoverRaw       bool
	recoverOverwrite bool
	recoverSafeNames string

	recoverCmd = &cobra.Command{
		Use: "recover",
		// The shared runner and dedup code log as for the daemon; on an
		// interactive recovery that is noise unless asked for.
		PersistentPreRun: func(*cobra.Command, []string) {
			if !recoverVerbose {
				log.SetOutput(io.Discard)
			}
			// Piping output into head or less must not kill a run before it
			// removes its scratch files: a closed pipe then fails the write
			// instead of ending the process.
			signal.Ignore(syscall.SIGPIPE)
		},
		Short: "Read backups straight from storage and extract files (no Vault server needed)",
		Long: `Recover files from Vault backups when the Vault server is not available.

recover connects to a backup destination directly, reads the backups'
manifests and extracts files into a local folder. It needs no Vault
database and runs on Linux, macOS and Windows. It never writes to, or
deletes from, the backup storage.

Connect with --type and either --path (local folders, including a mounted
network share such as Z:\backups or /mnt/backups) or --config-file (a JSON
file with the same fields as the destination's settings in Vault).

Deduplicated backups need --key, the vault.key file from the server that
made them (on Unraid: /boot/config/plugins/vault/vault.key). Encrypted
classic backups need the backup passphrase, read from --passphrase-file or
the VAULT_PASSPHRASE environment variable.

Typical use:
  vault recover list --path Z:\backups --key vault.key
  vault recover contents --path Z:\backups --key vault.key --point "Daily/latest" --item appdata
  vault recover extract --path Z:\backups --key vault.key --point "Daily/latest" --item appdata --to C:\recovered`,
	}

	recoverListCmd = &cobra.Command{
		Use:   "list",
		Short: "List the backups on the destination",
		Args:  cobra.NoArgs,
		RunE:  runRecoverList,
		// Errors here are about backups or connection details, not flags;
		// the usage text would only bury them.
		SilenceUsage: true,
	}

	recoverContentsCmd = &cobra.Command{
		Use:   "contents",
		Short: "List the files of one item in a backup",
		Args:  cobra.NoArgs,
		RunE:  runRecoverContents,
		// Errors here are about backups or connection details, not flags;
		// the usage text would only bury them.
		SilenceUsage: true,
	}

	recoverExtractCmd = &cobra.Command{
		Use:   "extract",
		Short: "Extract items from a backup into a local folder",
		Long: `Extract items from a backup into a local folder. Each item gets its
own subfolder of --to. Classic incremental and differential backups are
rebuilt from their whole chain.

On Windows, names Windows cannot store (for example "a:b", "CON", a
trailing dot) and names that differ only by case are given safe names; every
rename is printed and recorded in ` + recoverReportName + `.

Relative symbolic links that stay inside the item are recreated on Linux and
macOS; Windows skips all links. Absolute links, device files and file
ownership are never recreated. With --raw, each step of an incremental or
differential chain is copied into its own folder.`,
		Args: cobra.NoArgs,
		RunE: runRecoverExtract,
		// Errors here are about backups or connection details, not flags;
		// the usage text would only bury them.
		SilenceUsage: true,
	}
)

func init() {
	pf := recoverCmd.PersistentFlags()
	pf.StringVar(&recoverType, "type", "local", "storage type: local, s3, sftp, smb or webdav")
	pf.StringVar(&recoverPath, "path", "", "backup folder (local storage, or a mounted network share)")
	pf.StringVar(&recoverConfigFile, "config-file", "", "JSON file with the storage destination's settings")
	pf.StringVar(&recoverKeyFile, "key", "", "vault.key from the server that made the backups (needed for deduplicated backups)")
	pf.StringVar(&recoverPassphraseFile, "passphrase-file", "", "file holding the backup passphrase (default: $VAULT_PASSPHRASE)")
	pf.BoolVar(&recoverVerbose, "verbose", false, "show diagnostic log output")

	recoverListCmd.Flags().StringVar(&recoverListJob, "job", "", "only list backups of this job")
	recoverListCmd.Flags().BoolVar(&recoverJSON, "json", false, "print JSON")

	recoverContentsCmd.Flags().StringVar(&recoverPoint, "point", "", `backup to read, as printed by list ("<job>/<run>" or "<job>/latest")`)
	// StringArray, not StringSlice: item names and paths may contain commas.
	recoverContentsCmd.Flags().StringArrayVar(&recoverItems, "item", nil, "item to list")
	recoverContentsCmd.Flags().BoolVar(&recoverJSON, "json", false, "print JSON")
	_ = recoverContentsCmd.MarkFlagRequired("point")
	_ = recoverContentsCmd.MarkFlagRequired("item")

	ef := recoverExtractCmd.Flags()
	ef.StringVar(&recoverPoint, "point", "", `backup to extract from ("<job>/<run>" or "<job>/latest")`)
	ef.StringArrayVar(&recoverItems, "item", nil, "item to extract (repeatable; default: every item)")
	ef.StringArrayVar(&recoverIncludes, "include", nil, "only extract this path inside the item, as printed by contents (repeatable)")
	ef.StringVar(&recoverTo, "to", "", "local folder to extract into")
	ef.BoolVar(&recoverRaw, "raw", false, "copy classic backup archives out as-is (decrypted) instead of unpacking them")
	ef.BoolVar(&recoverOverwrite, "overwrite", false, "allow extracting into item folders that are not empty")
	ef.StringVar(&recoverSafeNames, "safe-names", "auto", "rewrite names Windows cannot store: auto (on Windows only), on, off (always on under Windows)")
	_ = recoverExtractCmd.MarkFlagRequired("point")
	_ = recoverExtractCmd.MarkFlagRequired("to")

	recoverCmd.AddCommand(recoverListCmd, recoverContentsCmd, recoverExtractCmd)
	rootCmd.AddCommand(recoverCmd)
}

// recoverOptions turns the connection flags into session options. Secrets
// come only from files or the environment, never from the command line,
// so they stay out of shell history and process listings.
func recoverOptions() (recovery.Options, error) {
	opts := recovery.Options{StorageType: strings.ToLower(strings.TrimSpace(recoverType))}
	switch {
	case recoverConfigFile != "" && recoverPath != "":
		return opts, errors.New("use either --path or --config-file, not both")
	case recoverConfigFile != "":
		body, err := os.ReadFile(recoverConfigFile) // #nosec G304 -- operator-supplied path
		if err != nil {
			return opts, fmt.Errorf("read --config-file: %w", err)
		}
		if !json.Valid(body) {
			return opts, fmt.Errorf("--config-file %s is not valid JSON", recoverConfigFile)
		}
		opts.StorageConfig = string(body)
	case recoverPath != "":
		if opts.StorageType != "local" {
			return opts, fmt.Errorf("--path is for local storage; use --config-file for %s", opts.StorageType)
		}
		body, _ := json.Marshal(map[string]string{"path": recoverPath})
		opts.StorageConfig = string(body)
	default:
		return opts, errors.New("say where the backups are: --path <folder> or --config-file <settings.json>")
	}
	if recoverKeyFile != "" {
		key, err := loadServerKeyAtPath(recoverKeyFile)
		if err != nil {
			return opts, fmt.Errorf("read --key: %w", err)
		}
		opts.ServerKey = key
	}
	switch {
	case recoverPassphraseFile != "":
		body, err := os.ReadFile(recoverPassphraseFile) // #nosec G304 -- operator-supplied path
		if err != nil {
			return opts, fmt.Errorf("read --passphrase-file: %w", err)
		}
		opts.Passphrase = trimLineEnding(string(body))
	default:
		opts.Passphrase = os.Getenv("VAULT_PASSPHRASE")
	}
	return opts, nil
}

// trimLineEnding removes the single line ending an editor or `echo` adds to
// a passphrase file, keeping any other characters, so a passphrase that
// itself ends in "\r" or "\n" survives.
func trimLineEnding(s string) string {
	if t, ok := strings.CutSuffix(s, "\n"); ok {
		return strings.TrimSuffix(t, "\r")
	}
	return s
}

func openRecoverSession() (*recovery.Session, error) {
	opts, err := recoverOptions()
	if err != nil {
		return nil, err
	}
	return recovery.Open(opts)
}

func runRecoverList(cmd *cobra.Command, _ []string) error {
	s, err := openRecoverSession()
	if err != nil {
		return err
	}
	defer s.Close()
	points, err := s.Points()
	if err != nil {
		return err
	}
	if recoverListJob != "" {
		filtered := points[:0]
		for _, p := range points {
			if p.Job == recoverListJob {
				filtered = append(filtered, p)
			}
		}
		points = filtered
	}
	out := cmd.OutOrStdout()
	if recoverJSON {
		return writeJSON(out, points)
	}
	if len(points) == 0 {
		_, _ = fmt.Fprintln(out, "No Vault backups found at this location.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "POINT\tCREATED\tTYPE\tENCRYPTION\tITEMS")
	locked := 0
	for _, p := range points {
		items := make([]string, 0, len(p.Items))
		for _, it := range p.Items {
			items = append(items, it.Name+" ("+it.Type+")")
		}
		enc := p.Encryption
		if p.IsDedup() {
			enc = "dedup"
		}
		if p.Locked {
			enc = "locked (" + map[string]string{"dedup": "needs --key", "age": "needs passphrase"}[p.LockedBy] + ")"
			locked++
		}
		created := "-"
		if !p.CreatedAt.IsZero() {
			created = p.CreatedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.StoragePath, created, orDash(p.BackupType), orDash(enc), strings.Join(items, ", "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if locked > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "\n%d backup(s) are locked: deduplicated backups need --key (vault.key from the original server); encrypted classic backups need the backup passphrase.\n", locked)
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "\nUse a POINT value with --point, or <job>/latest for a job's newest backup.")
	return nil
}

func runRecoverContents(cmd *cobra.Command, _ []string) error {
	if len(recoverItems) != 1 {
		return errors.New("contents takes exactly one --item")
	}
	s, err := openRecoverSession()
	if err != nil {
		return err
	}
	defer s.Close()
	p, err := s.FindPoint(recoverPoint)
	if err != nil {
		return err
	}
	entries, err := s.Contents(p, recoverItems[0])
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if recoverJSON {
		return writeJSON(out, entries)
	}
	var total int64
	files := 0
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(out, "%12s  %s/\n", "", e.Path)
			continue
		}
		files++
		total += e.Size
		fmt.Fprintf(out, "%12s  %s\n", format.Bytes(float64(e.Size)), e.Path)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "\n%d files, %s\n", files, format.Bytes(float64(total)))
	return nil
}

func runRecoverExtract(cmd *cobra.Command, _ []string) error {
	var safe *bool
	switch strings.ToLower(recoverSafeNames) {
	case "auto", "":
	case "on", "true", "yes":
		v := true
		safe = &v
	case "off", "false", "no":
		v := false
		safe = &v
	default:
		return fmt.Errorf("--safe-names must be auto, on or off")
	}
	s, err := openRecoverSession()
	if err != nil {
		return err
	}
	defer s.Close()
	p, err := s.FindPoint(recoverPoint)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errOut := cmd.ErrOrStderr()
	rep, err := s.Extract(ctx, p, recovery.ExtractOptions{
		Items:     recoverItems,
		Include:   recoverIncludes,
		Dest:      recoverTo,
		Raw:       recoverRaw,
		SafeNames: safe,
		Overwrite: recoverOverwrite,
		Progress:  func(line string) { _, _ = fmt.Fprintln(errOut, line) },
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	for _, it := range rep.Items {
		if it.Error != "" {
			fmt.Fprintf(out, "%s: FAILED: %s\n", it.Item, it.Error)
			continue
		}
		fmt.Fprintf(out, "%s: %d files, %s -> %s\n", it.Item, it.Files, format.Bytes(float64(it.Bytes)), it.Dir)
		printLimited(out, "  renamed", len(it.Renamed), func(i int) string {
			return it.Renamed[i].From + " -> " + it.Renamed[i].To
		})
		printLimited(out, "  not recreated", len(it.Skipped), func(i int) string {
			return it.Skipped[i].Path + ": " + it.Skipped[i].Reason
		})
	}
	reportPath := filepath.Join(recoverTo, "vault-recover-report-"+time.Now().Format("20060102-150405")+".json")
	body, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		err = os.WriteFile(reportPath, body, 0o600)
	}
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write the report to %s: %v\n", reportPath, err)
	} else {
		fmt.Fprintf(out, "Report: %s\n", reportPath)
	}
	if rep.Failed() {
		return errors.New("some items could not be recovered")
	}
	return nil
}

// printLimited prints a heading and up to 20 lines, pointing at the report
// for the rest.
func printLimited(out io.Writer, heading string, n int, line func(int) string) {
	if n == 0 {
		return
	}
	fmt.Fprintf(out, "%s (%d):\n", heading, n)
	for i := 0; i < n && i < 20; i++ {
		fmt.Fprintf(out, "    %s\n", line(i))
	}
	if n > 20 {
		fmt.Fprintf(out, "    … and %d more, listed in %s\n", n-20, recoverReportName)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
