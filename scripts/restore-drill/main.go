package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	const usage = "usage: restore-drill folder-create|folder-check <directory> <manifest> or inspect|request|negative|follow|withdraw [flags]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "folder-create", "folder-check":
		if len(args) != 3 {
			return errors.New(usage)
		}
		if args[0] == "folder-create" {
			return createFixture(args[1], args[2])
		}
		return checkFixture(args[1], args[2], output)
	case "inspect", "request", "negative", "follow", "withdraw":
		return runMCP(ctx, args[0], args[1:], output)
	default:
		return errors.New(usage)
	}
}
