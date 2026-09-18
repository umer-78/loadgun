// Package load drives the requests.
//
// The design is deliberately boring: a fixed pool of goroutines, one channel
// handing out work, one channel collecting results. There is no shared counter
// being incremented from many goroutines, so there is nothing to race on and the
// numbers cannot drift.
package load

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/umer-78/loadgun/internal/report"
)

// Options configures a run. Either Requests or Duration must be set.
type Options struct {
	URL         string
	Method      string
	Body        []byte
	Headers     http.Header
	Concurrency int
	Requests    int           // total requests to send; ignored when Duration is set
	Duration    time.Duration // run for this long instead of a fixed count
	Rate        float64       // requests per second across all workers; 0 means as fast as possible
	Timeout     time.Duration // per-request timeout
	Insecure    bool          // skip TLS certificate verification
	KeepAlive   bool
}

// Validate fills in defaults and reports anything that cannot work.
func (o *Options) Validate() error {
	if o.URL == "" {
		return errors.New("a URL is required")
	}
	parsed, err := url.Parse(o.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%q is not an absolute http(s) URL", o.URL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q — loadgun speaks http and https", parsed.Scheme)
	}
	if o.Method == "" {
		o.Method = http.MethodGet
	}
	o.Method = strings.ToUpper(o.Method)
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Rate < 0 {
		return errors.New("rate cannot be negative")
	}
	if o.Duration <= 0 && o.Requests <= 0 {
		return errors.New("set either a number of requests or a duration")
	}
	if o.Duration <= 0 && o.Requests < o.Concurrency {
		// More workers than requests is not an error, but it is never what the
		// person meant, and it makes the concurrency figure in the report a lie.
		o.Concurrency = o.Requests
	}
	return nil
}

// Runner executes one load run.
type Runner struct {
	opts   Options
	client *http.Client
}

// New builds a Runner, validating and defaulting the options.
func New(opts Options) (*Runner, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	transport := &http.Transport{
		// One idle connection per worker, so workers are not queueing behind a
		// shared pool and measuring the queue instead of the server.
		MaxIdleConns:        opts.Concurrency * 2,
		MaxIdleConnsPerHost: opts.Concurrency * 2,
		DisableKeepAlives:   !opts.KeepAlive,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: opts.Insecure}, //nolint:gosec // opt-in via --insecure
	}

	return &Runner{
		opts: opts,
		client: &http.Client{
			Transport: transport,
			Timeout:   opts.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				// Following a redirect would measure two requests as one.
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Options returns the validated options, with defaults applied.
func (r *Runner) Options() Options { return r.opts }

// Run sends the requests and returns every attempt, in completion order, with
// the wall-clock time the run took. Cancelling ctx stops it early and keeps
// whatever has already been measured.
func (r *Runner) Run(ctx context.Context) ([]report.Attempt, time.Duration, error) {
	// The duration limit stops the dispatcher handing out new work; it does not
	// cancel requests already in flight. Cutting a request off at the deadline
	// would record it as a failure of the target, when all that happened is that
	// the test ended — so the last few requests get to finish, bounded by the
	// per-request timeout.
	dispatch := ctx
	if r.opts.Duration > 0 {
		var cancelTimer context.CancelFunc
		dispatch, cancelTimer = context.WithTimeout(ctx, r.opts.Duration)
		defer cancelTimer()
	}

	work := make(chan struct{})
	results := make(chan report.Attempt, r.opts.Concurrency)

	var ticks <-chan time.Time
	if r.opts.Rate > 0 {
		ticker := time.NewTicker(time.Duration(float64(time.Second) / r.opts.Rate))
		defer ticker.Stop()
		ticks = ticker.C
	}

	started := time.Now()

	go func() {
		defer close(work)
		for sent := 0; r.opts.Duration > 0 || sent < r.opts.Requests; sent++ {
			if ticks != nil {
				select {
				case <-ticks:
				case <-dispatch.Done():
					return
				}
			}
			select {
			case work <- struct{}{}:
			case <-dispatch.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < r.opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range work {
				results <- r.once(ctx)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	attempts := make([]report.Attempt, 0, r.opts.Requests)
	for attempt := range results {
		// A request the operator cancelled (Ctrl-C) never got a fair chance, so
		// it is not evidence about the target and is left out of the report.
		if attempt.Err != nil && errors.Is(attempt.Err, context.Canceled) {
			continue
		}
		attempts = append(attempts, attempt)
	}

	return attempts, time.Since(started), nil
}

// once sends a single request and measures it.
func (r *Runner) once(ctx context.Context) report.Attempt {
	var body io.Reader
	if len(r.opts.Body) > 0 {
		body = bytes.NewReader(r.opts.Body)
	}

	request, err := http.NewRequestWithContext(ctx, r.opts.Method, r.opts.URL, body)
	if err != nil {
		return report.Attempt{Err: err}
	}
	for name, values := range r.opts.Headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if request.Header.Get("User-Agent") == "" {
		request.Header.Set("User-Agent", "loadgun")
	}

	start := time.Now()
	response, err := r.client.Do(request)
	if err != nil {
		return report.Attempt{Latency: time.Since(start), Err: err}
	}
	defer response.Body.Close()

	// The body must be drained before the clock stops: a response is not
	// received until its body is, and draining is also what lets the connection
	// be reused instead of being thrown away.
	read, err := io.Copy(io.Discard, response.Body)
	latency := time.Since(start)

	return report.Attempt{Latency: latency, Status: response.StatusCode, Bytes: read, Err: err}
}
