package main

import "fmt"

// version é definida no build: -ldflags "-X main.version=v0.1.0"
var version = "dev"

func printVersion() {
	fmt.Printf("meloz-nano-cli %s\n", version)
}
