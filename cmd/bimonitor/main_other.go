//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "A BI Output Monitor Windows alkalmazás. Fejlesztéshez: go run ./cmd/devserver")
	os.Exit(1)
}
