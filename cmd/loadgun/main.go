// Command loadgun sends HTTP load at a URL and reports what came back.
//
//	loadgun -n 500 -c 20 https://example.com
//	loadgun -d 30s -c 50 --rate 100 --format json https://example.com/api
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/umer-78/loadgun/internal/cli"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ctrl-C stops the run but still prints the report: an interrupted load test
	// that throws away its measurements is worse than useless.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		cancel()
	}()

	os.Exit(cli.Main(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
