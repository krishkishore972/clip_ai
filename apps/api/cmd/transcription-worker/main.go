package main

import (
	"fmt"
	"os"

	"github.com/krishkishore972/clip_ai/internal/app/transcriptionworker"
)

func main() {
	if err := transcriptionworker.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
