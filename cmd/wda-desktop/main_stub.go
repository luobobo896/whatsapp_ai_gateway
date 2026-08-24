//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "WDAFarmGateway desktop shell is Windows-only. On macOS use desktop/ (Swift) + scripts/build-dmg.sh.")
	os.Exit(1)
}
