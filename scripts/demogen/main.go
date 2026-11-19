//go:build demogen

// Demo frame generator: renders the TUI journeys with canned school data.
// Usage: go run -tags demogen ./scripts/demogen [OUTPUT_DIR]
package main

import (
	"fmt"
	"os"

	"bupt.local/cli/internal/tui"
)

func main() {
	dir := "assets/demo"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := tui.GenerateDemoFrames(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(dir + "/journey.txt")
}
