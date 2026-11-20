package main

import (
	"fmt"
	"os"

	"github.com/krishkishore972/clip_ai/internal/app/mediaworker"
)

func main() {
	if err := mediaworker.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "media worker startup or shutdown failed: %v\n", err)
		os.Exit(1)
	}
}
