package main

import (
	"fmt"
	"os"

	"github.com/krishkishore972/clip_ai/internal/app/curationworker"
)

func main() {
	if err := curationworker.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
