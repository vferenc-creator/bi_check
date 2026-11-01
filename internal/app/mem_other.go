//go:build !windows

package app

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// physMemory returns the free and total physical memory (Linux: /proc/meminfo).
func physMemory() (avail, total uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemAvailable":
			avail = n << 10
		case "MemTotal":
			total = n << 10
		}
	}
	return avail, total, total > 0
}
