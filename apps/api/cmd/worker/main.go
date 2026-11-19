// Command worker delegates to the importable application package.
package main

import (
	"fmt"
	"os"

	"github.com/krishkishore972/clip_ai/internal/app/worker"
)

func main() {
	if err := worker.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
