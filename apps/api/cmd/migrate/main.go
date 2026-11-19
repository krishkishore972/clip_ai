// Command migrate delegates to the importable application package.
package main

import (
	"fmt"
	"os"

	"github.com/krishkishore972/clip_ai/internal/app/migrate"
)

func main() {
	if err := migrate.Run(os.Args[1:], os.Getenv("DATABASE_URL"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
