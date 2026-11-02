//go:build !krun

package main

import "fmt"

func krunBoot(args []string) error {
	return fmt.Errorf("backend libkrun não compilado (use go build -tags krun)")
}
