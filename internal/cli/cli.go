// Package cli parses the flags and prints the report.
//
// Main takes its streams as arguments rather than writing to os.Stdout directly,
// so the tests drive the real command and read back exactly what a user sees.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/umer-78/loadgun/internal/load"
	"github.com/umer-78/loadgun/internal/report"
)

const usage = `loadgun — HTTP load testing

  loadgun [options] <url>

Options:
  -n, --requests int       total requests to send (default 200)
  -d, --duration dur       run for this long instead of a fixed count
  -c, --concurrency int    workers sending at once (default 10)
      --rate float         cap at this many requests per second (0 = no cap)
  -m, --method string      HTTP method (default GET)
  -H, --header k:v         request header, repeatable
  -b, --body string        request body
      --timeout dur        per-request timeout (default 10s)
      --keepalive          reuse connections between requests
      --insecure           skip TLS certificate verification
      --format string      table, json or csv (default table)
  -o, --out file           write the report here instead of stdout
      --version

Examples:
  loadgun -n 500 -c 20 http://localhost:8080/health
  loadgun -d 30s -c 50 --rate 100 --format json https://example.com/api
`

// Version is the released version, overridable at build time with -ldflags.
var Version = "1.0.0"

type headerList []string

func (h *headerList) String() string { return strings.Join(*h, ", ") }

func (h *headerList) Set(value string) error {
	if !strings.Contains(value, ":") {
		return fmt.Errorf("header %q must look like Name: value", value)
	}
	*h = append(*h, value)
	return nil
}

// Main runs the command and returns the process exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loadgun", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }

	var (
		requests    = flags.Int("requests", 200, "")
		duration    = flags.Duration("duration", 0, "")
		concurrency = flags.Int("concurrency", 10, "")
		rate        = flags.Float64("rate", 0, "")
		method      = flags.String("method", "GET", "")
		body        = flags.String("body", "", "")
		timeout     = flags.Duration("timeout", 10*time.Second, "")
		keepalive   = flags.Bool("keepalive", false, "")
		insecure    = flags.Bool("insecure", false, "")
		format      = flags.String("format", "table", "")
		out         = flags.String("out", "", "")
		version     = flags.Bool("version", false, "")
		headers     headerList
	)
	flags.Var(&headers, "header", "")

	// Short aliases, so -n and -c behave the way every other load tool's do.
	for long, short := range map[string]string{
		"requests": "n", "duration": "d", "concurrency": "c", "method": "m",
		"header": "H", "body": "b", "out": "o",
	} {
		flags.Var(flags.Lookup(long).Value, short, "")
	}

	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *version {
		fmt.Fprintf(stdout, "loadgun %s\n", Version)
		return 0
	}

	if flags.NArg() != 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	parsedHeaders := http.Header{}
	for _, raw := range headers {
		name, value, _ := strings.Cut(raw, ":")
		parsedHeaders.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}

	if *format != "table" && *format != "json" && *format != "csv" {
		fmt.Fprintf(stderr, "loadgun: unknown format %q — use table, json or csv\n", *format)
		return 2
	}

	runner, err := load.New(load.Options{
		URL:         flags.Arg(0),
		Method:      *method,
		Body:        []byte(*body),
		Headers:     parsedHeaders,
		Concurrency: *concurrency,
		Requests:    *requests,
		Duration:    *duration,
		Rate:        *rate,
		Timeout:     *timeout,
		Insecure:    *insecure,
		KeepAlive:   *keepalive,
	})
	if err != nil {
		fmt.Fprintf(stderr, "loadgun: %v\n", err)
		return 2
	}

	opts := runner.Options()
	if *format == "table" {
		fmt.Fprintf(stderr, "%s %s — %d workers", opts.Method, opts.URL, opts.Concurrency)
		if opts.Duration > 0 {
			fmt.Fprintf(stderr, ", %s", opts.Duration)
		} else {
			fmt.Fprintf(stderr, ", %d requests", opts.Requests)
		}
		if opts.Rate > 0 {
			fmt.Fprintf(stderr, ", capped at %.0f/s", opts.Rate)
		}
		fmt.Fprintln(stderr)
	}

	attempts, elapsed, err := runner.Run(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "loadgun: %v\n", err)
		return 1
	}
	if len(attempts) == 0 {
		fmt.Fprintln(stderr, "loadgun: no requests completed")
		return 1
	}

	writer := stdout
	if *out != "" {
		file, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(stderr, "loadgun: %v\n", err)
			return 1
		}
		defer file.Close()
		writer = file
	}

	summary := report.Summarize(attempts, elapsed)

	switch *format {
	case "json":
		encoded, err := report.JSON(summary)
		if err != nil {
			fmt.Fprintf(stderr, "loadgun: %v\n", err)
			return 1
		}
		fmt.Fprintln(writer, encoded)
	case "csv":
		if err := report.CSV(writer, attempts); err != nil {
			fmt.Fprintf(stderr, "loadgun: %v\n", err)
			return 1
		}
	default:
		fmt.Fprint(writer, report.Text(summary))
	}

	// A run where anything failed exits non-zero, so it works as a CI gate.
	if summary.Failed > 0 {
		return 1
	}
	return 0
}
