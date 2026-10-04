//go:build !windows

package main

// osUILanguage is the Windows-only OS UI language probe; on unix the LANG /
// LANGUAGE environment variables already carry the locale.
func osUILanguage() string { return "" }
