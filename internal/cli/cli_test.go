package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serve(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	if handler == nil {
		handler = func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Main(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestASuccessfulRunExitsZeroAndPrintsTheReport(t *testing.T) {
	code, stdout, stderr := run("-n", "10", "-c", "2", serve(t, nil))

	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, stderr)
	}
	for _, want := range []string{"Requests    10", "Succeeded   10 (100.0%)", "Latency", "Status codes"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

func TestTheRunHeaderGoesToStderrSoStdoutStaysParseable(t *testing.T) {
	_, stdout, stderr := run("-n", "4", "-c", "2", serve(t, nil))

	if !strings.Contains(stderr, "2 workers") {
		t.Errorf("stderr should describe the run, got:\n%s", stderr)
	}
	if strings.Contains(stdout, "workers") {
		t.Errorf("stdout should hold only the report, got:\n%s", stdout)
	}
}

func TestAFailingTargetExitsOneSoItWorksAsACIGate(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})

	code, stdout, _ := run("-n", "5", "-c", "1", url)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stdout, "Failed      5") {
		t.Errorf("got:\n%s", stdout)
	}
}

func TestJSONOutputIsValidAndComplete(t *testing.T) {
	code, stdout, _ := run("-n", "6", "-c", "2", "--format", "json", serve(t, nil))

	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	if parsed["total"].(float64) != 6 {
		t.Errorf("total = %v, want 6", parsed["total"])
	}
	if _, ok := parsed["latency_ms"]; !ok {
		t.Error("latency_ms is missing")
	}
}

func TestCSVOutputHasARowPerRequest(t *testing.T) {
	_, stdout, _ := run("-n", "7", "-c", "1", "--format", "csv", serve(t, nil))

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 8 {
		t.Fatalf("got %d lines, want header + 7:\n%s", len(lines), stdout)
	}
	if !strings.HasPrefix(lines[0], "n,latency_ms") {
		t.Errorf("header = %q", lines[0])
	}
}

func TestOutWritesTheReportToAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")

	code, stdout, _ := run("-n", "3", "-c", "1", "--format", "json", "-o", path, serve(t, nil))

	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty when -o is used, got:\n%s", stdout)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), `"total": 3`) {
		t.Errorf("file holds:\n%s", written)
	}
}

func TestShortAndLongFlagsAgree(t *testing.T) {
	url := serve(t, nil)

	_, short, _ := run("-n", "4", "-c", "2", url)
	_, long, _ := run("--requests", "4", "--concurrency", "2", url)

	if !strings.Contains(short, "Requests    4") || !strings.Contains(long, "Requests    4") {
		t.Errorf("short:\n%s\nlong:\n%s", short, long)
	}
}

func TestHeadersAreSentAndMalformedOnesAreRejected(t *testing.T) {
	var got string
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Token")
		w.Write([]byte("ok"))
	})

	if code, _, _ := run("-n", "1", "-H", "X-Token: abc", url); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got != "abc" {
		t.Errorf("X-Token = %q", got)
	}

	if code, _, stderr := run("-n", "1", "-H", "no-colon-here", url); code != 2 {
		t.Errorf("a malformed header should exit 2, got %d: %s", code, stderr)
	}
}

func TestMissingOrExtraArgumentsPrintUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"http://a", "http://b"}} {
		code, _, stderr := run(args...)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
		if !strings.Contains(stderr, "loadgun — HTTP load testing") {
			t.Errorf("%v: expected usage, got:\n%s", args, stderr)
		}
	}
}

func TestAnUnknownFormatIsRefusedBeforeAnyLoadIsSent(t *testing.T) {
	var hits int
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte("ok"))
	})

	code, _, stderr := run("-n", "5", "--format", "yaml", url)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown format") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if hits != 0 {
		t.Errorf("server was hit %d times before the flag was validated", hits)
	}
}

func TestABadURLIsReportedWithoutAStackTrace(t *testing.T) {
	code, _, stderr := run("-n", "1", "ftp://example.com")

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unsupported scheme") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestVersionPrintsAndExitsZero(t *testing.T) {
	code, stdout, _ := run("--version")

	if code != 0 {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(stdout, "loadgun ") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestACancelledContextStillReportsWhatFinished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out, errOut bytes.Buffer
	code := Main(ctx, []string{"-n", "50", "-c", "2", serve(t, nil)}, &out, &errOut)

	// Nothing completed, so there is nothing to report — and that is stated
	// rather than printed as a summary of zero requests.
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "no requests completed") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
}
