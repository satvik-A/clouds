// Package main provides the CloudFS CLI entry point.
// CloudFS is a provider-agnostic, policy-driven cloud storage control plane.
package main

import (
	"fmt"
	"os"

	"github.com/cloudfs/cloudfs/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
