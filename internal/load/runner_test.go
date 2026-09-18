package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// counting returns a server that records how many requests it handled and the
// highest number it was serving at once.
func counting(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (*httptest.Server, *int64, *int64) {
	t.Helper()
	var total, inFlight, peak int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&total, 1)
		now := atomic.AddInt64(&inFlight, 1)
		for {
			high := atomic.LoadInt64(&peak)
			if now <= high || atomic.CompareAndSwapInt64(&peak, high, now) {
				break
			}
		}
		defer atomic.AddInt64(&inFlight, -1)

		if handler != nil {
			handler(w, r)
			return
		}
		w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	return server, &total, &peak
}

func TestValidateFillsInDefaults(t *testing.T) {
	opts := Options{URL: "http://example.com", Requests: 10}

	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	if opts.Method != "GET" {
		t.Errorf("method = %q, want GET", opts.Method)
	}
	if opts.Concurrency != 1 {
		t.Errorf("concurrency = %d, want 1", opts.Concurrency)
	}
	if opts.Timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", opts.Timeout)
	}
}

func TestValidateRejectsWhatCannotWork(t *testing.T) {
	cases := map[string]Options{
		"no url":        {Requests: 1},
		"not a url":     {URL: "not a url", Requests: 1},
		"relative":      {URL: "/health", Requests: 1},
		"wrong scheme":  {URL: "ftp://example.com", Requests: 1},
		"negative rate": {URL: "http://example.com", Requests: 1, Rate: -1},
		"nothing to do": {URL: "http://example.com"},
	}
	for name, opts := range cases {
		if err := opts.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidateLowersConcurrencyToTheRequestCount(t *testing.T) {
	// 50 workers for 5 requests would report a concurrency that never happened.
	opts := Options{URL: "http://example.com", Requests: 5, Concurrency: 50}

	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	if opts.Concurrency != 5 {
		t.Errorf("concurrency = %d, want 5", opts.Concurrency)
	}
}

func TestMethodIsUppercased(t *testing.T) {
	opts := Options{URL: "http://example.com", Requests: 1, Method: "post"}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	if opts.Method != "POST" {
		t.Errorf("method = %q", opts.Method)
	}
}

func TestRunSendsExactlyTheRequestedNumber(t *testing.T) {
	server, total, _ := counting(t, nil)

	runner, err := New(Options{URL: server.URL, Requests: 47, Concurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	attempts, elapsed, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(attempts) != 47 {
		t.Errorf("measured %d attempts, want 47", len(attempts))
	}
	if got := atomic.LoadInt64(total); got != 47 {
		t.Errorf("server saw %d requests, want 47", got)
	}
	if elapsed <= 0 {
		t.Error("elapsed should be positive")
	}
}

func TestRunNeverExceedsTheConcurrencyAsked(t *testing.T) {
	server, _, peak := counting(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond) // hold the connection so overlap is visible
		w.Write([]byte("ok"))
	})

	runner, err := New(Options{URL: server.URL, Requests: 40, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt64(peak); got > 4 {
		t.Errorf("server saw %d concurrent requests, want at most 4", got)
	}
	if got := atomic.LoadInt64(peak); got < 2 {
		t.Errorf("peak concurrency was %d — the workers are not running in parallel", got)
	}
}

func TestRateLimitPacesTheRun(t *testing.T) {
	server, total, _ := counting(t, nil)

	// 20 requests at 100/s cannot finish in less than ~190ms however many
	// workers are free.
	runner, err := New(Options{URL: server.URL, Requests: 20, Concurrency: 10, Rate: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, elapsed, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if elapsed < 150*time.Millisecond {
		t.Errorf("run took %v — the rate cap is not being applied", elapsed)
	}
	if got := atomic.LoadInt64(total); got != 20 {
		t.Errorf("server saw %d requests, want 20", got)
	}
}

func TestDurationModeStopsOnTime(t *testing.T) {
	server, _, _ := counting(t, nil)

	runner, err := New(Options{URL: server.URL, Duration: 150 * time.Millisecond, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	attempts, elapsed, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if elapsed > time.Second {
		t.Errorf("run took %v, want about 150ms", elapsed)
	}
	if len(attempts) == 0 {
		t.Error("expected some requests in 150ms")
	}
}

func TestCancellingKeepsWhatWasAlreadyMeasured(t *testing.T) {
	server, _, _ := counting(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.Write([]byte("ok"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	runner, err := New(Options{URL: server.URL, Requests: 100000, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	attempts, elapsed, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(attempts) == 0 {
		t.Error("cancelling threw away every measurement")
	}
	if len(attempts) >= 100000 {
		t.Error("cancelling did not stop the run")
	}
	if elapsed > 3*time.Second {
		t.Errorf("run took %v after cancel at 100ms", elapsed)
	}
}

func TestNonSuccessResponsesAreRecordedNotDropped(t *testing.T) {
	server, _, _ := counting(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	})

	runner, _ := New(Options{URL: server.URL, Requests: 5, Concurrency: 1})
	attempts, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for i, attempt := range attempts {
		if attempt.Status != http.StatusServiceUnavailable {
			t.Errorf("attempt %d: status %d, want 503", i, attempt.Status)
		}
		if attempt.OK() {
			t.Errorf("attempt %d: a 503 must not count as a success", i)
		}
	}
}

func TestATimeoutIsRecordedAsAnError(t *testing.T) {
	server, _, _ := counting(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte("late"))
	})

	runner, _ := New(Options{URL: server.URL, Requests: 2, Concurrency: 2, Timeout: 50 * time.Millisecond})
	attempts, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for i, attempt := range attempts {
		if attempt.Err == nil {
			t.Errorf("attempt %d: expected a timeout error", i)
		}
	}
}

func TestHeadersMethodAndBodyReachTheServer(t *testing.T) {
	var (
		gotMethod string
		gotToken  string
		gotBody   string
	)
	server, _, _ := counting(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotToken = r.Header.Get("X-Token")
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		gotBody = string(body)
		w.Write([]byte("ok"))
	})

	runner, err := New(Options{
		URL:         server.URL,
		Method:      "post",
		Body:        []byte(`{"hello":"world"}`),
		Headers:     http.Header{"X-Token": []string{"abc123"}},
		Requests:    1,
		Concurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotToken != "abc123" {
		t.Errorf("X-Token = %q", gotToken)
	}
	if !strings.Contains(gotBody, "hello") {
		t.Errorf("body = %q", gotBody)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write([]byte("final"))
	}))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()

	runner, _ := New(Options{URL: server.URL, Requests: 3, Concurrency: 1})
	attempts, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Following the redirect would measure two requests as one.
	for i, attempt := range attempts {
		if attempt.Status != http.StatusFound {
			t.Errorf("attempt %d: status %d, want 302", i, attempt.Status)
		}
	}
	if atomic.LoadInt64(&hits) != 0 {
		t.Error("the redirect was followed")
	}
}

func TestBodyBytesAreCounted(t *testing.T) {
	server, _, _ := counting(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 512)))
	})

	runner, _ := New(Options{URL: server.URL, Requests: 4, Concurrency: 2})
	attempts, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for i, attempt := range attempts {
		if attempt.Bytes != 512 {
			t.Errorf("attempt %d: read %d bytes, want 512", i, attempt.Bytes)
		}
	}
}

func TestTheDurationLimitDoesNotFailInFlightRequests(t *testing.T) {
	// Every response takes 80ms and the run lasts 200ms, so requests are
	// certainly in flight when the clock runs out. None of them may be recorded
	// as a failure: the test ended, the server did nothing wrong.
	server, _, _ := counting(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Write([]byte("ok"))
	})

	runner, err := New(Options{URL: server.URL, Duration: 200 * time.Millisecond, Concurrency: 4, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	attempts, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(attempts) == 0 {
		t.Fatal("expected some requests")
	}
	for i, attempt := range attempts {
		if attempt.Err != nil {
			t.Errorf("attempt %d failed with %v — the deadline must not cut requests off", i, attempt.Err)
		}
	}
}

func TestCancelledRequestsAreNotCountedAsFailures(t *testing.T) {
	server, _, _ := counting(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte("ok"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	runner, _ := New(Options{URL: server.URL, Requests: 10000, Concurrency: 4})

	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	attempts, _, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for i, attempt := range attempts {
		if attempt.Err != nil && strings.Contains(attempt.Err.Error(), "context canceled") {
			t.Errorf("attempt %d: a cancelled request should be left out, not reported as a failure", i)
		}
	}
}
