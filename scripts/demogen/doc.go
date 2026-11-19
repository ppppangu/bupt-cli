// Command demogen renders scripted TUI journeys into ANSI frames for the
// recorded demos. It only builds with -tags demogen:
//
//	go run -tags demogen ./scripts/demogen assets/demo
//
// The demo data is canned and never touches real accounts.
package main
