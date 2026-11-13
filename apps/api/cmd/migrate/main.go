// Command migrate explicitly applies or inspects Goose migrations.
// It deliberately exposes no destructive rollback command.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/migrations"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := run(os.Args[1:], os.Getenv("DATABASE_URL"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, databaseURL string, out io.Writer) error {
	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		return errors.New("usage: migrate up|status")
	}
	if strings.TrimSpace(databaseURL) == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := db.Connect(databaseURL)
	if err != nil {
		// Raw driver errors can contain connection credentials.
		return errors.New("database connection failed; check DATABASE_URL and PostgreSQL availability")
	}
	defer pool.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, pool, migrations.Files)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if args[0] == "status" {
		statuses, err := provider.Status(ctx)
		if err != nil {
			return errors.New("migration status failed; check database access")
		}
		for _, status := range statuses {
			fmt.Fprintf(out, "%05d %s\n", status.Source.Version, status.State)
		}
		return nil
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return errors.New("migration failed; check schema conflicts, permissions, and migration SQL")
	}
	for _, result := range results {
		fmt.Fprintln(out, result)
	}
	if len(results) == 0 {
		fmt.Fprintln(out, "Database is up to date.")
	}
	return nil
}
