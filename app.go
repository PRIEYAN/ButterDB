//go:build ignore

package main

import (
	"os"
	"os/exec"
)

func main() {
	cmd := exec.Command("go", append([]string{"run", "./apps/admin"}, os.Args[1:]...)...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
