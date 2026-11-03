//go:build !krun

package main

import "fmt"

func krunBoot(args []string) error {
	return fmt.Errorf("backend libkrun não compilado (use go build -tags krun)")
}

func runKrunDaemon(_, _, _, _, _ string) error {
	return fmt.Errorf("NANOC_BACKEND=krun pede um nanoc compilado com -tags krun")
}
