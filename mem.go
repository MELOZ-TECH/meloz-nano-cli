package main

import (
	"os"
	"strconv"
)

// memBytes: RAM da VM em bytes; NANO_MEM_MB (default 1024).
func memBytes() uint64 {
	mb := 1024
	if v := os.Getenv("NANO_MEM_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 256 {
			mb = n
		}
	}
	return uint64(mb) * 1024 * 1024
}
