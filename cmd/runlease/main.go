package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Errors are printed here, not by cobra, so a cloud SDK error can carry a
	// line saying what to do about it.
	rootCmd.SilenceErrors = true
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", explainCloudError(err))
		os.Exit(1)
	}
}
