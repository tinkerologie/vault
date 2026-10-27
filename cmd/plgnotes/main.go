// Command plgnotes prepares version-specific Unraid plugin release notes.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	os.Exit(exitCode(os.Args[1:], os.Stderr))
}

func exitCode(args []string, stderr io.Writer) int {
	if err := run(args); err != nil {
		_, _ = fmt.Fprintln(stderr, "plgnotes:", err)
		return 1
	}
	return 0
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected extract or update-plg subcommand")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	version := flags.String("version", "", "release version without v prefix")
	var input, output, notes *string
	switch args[0] {
	case "extract":
		input = flags.String("changelog", "", "source changelog")
		output = flags.String("output", "", "output Markdown file")
	case "update-plg":
		input = flags.String("manifest", "", "plugin manifest to update")
		notes = flags.String("notes", "", "extracted release notes file")
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *version == "" || *input == "" ||
		(output != nil && *output == "") || (notes != nil && *notes == "") {
		return fmt.Errorf("%s requires --version and all input/output path flags, without positional arguments", args[0])
	}
	content, err := os.ReadFile(*input)
	if err != nil {
		return fmt.Errorf("read %s: %w", *input, err)
	}
	if args[0] == "extract" {
		result, err := extractNotes(string(content), *version)
		if err != nil {
			return err
		}
		return atomicWrite(*output, result, 0o644)
	}
	body, err := os.ReadFile(*notes)
	if err != nil {
		return fmt.Errorf("read notes: %w", err)
	}
	result, err := replaceChanges(string(content), string(body), *version)
	if err != nil {
		return err
	}
	if result == string(content) {
		return nil
	}
	info, err := os.Stat(*input)
	if err != nil {
		return fmt.Errorf("stat manifest: %w", err)
	}
	return atomicWrite(*input, result, info.Mode().Perm())
}

// Stage beside the destination so rename cannot expose a partially written file.
func atomicWrite(path, content string, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".plgnotes-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err := io.WriteString(f, content); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
