package report

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func msec(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func durations(values ...int) []time.Duration {
	out := make([]time.Duration, len(values))
	for i, v := range values {
		out[i] = msec(v)
	}
	return out
}

func TestPercentileUsesNearestRank(t *testing.T) {
	// Ten values, 10ms to 100ms. Nearest rank puts p50 at the 5th value (50ms),
	// p90 at the 9th (90ms) and p95 at the 10th (100ms) — never between two
	// measurements that never happened.
	sorted := durations(10, 20, 30, 40, 50, 60, 70, 80, 90, 100)

	for _, tc := range []struct {
		p    float64
		want time.Duration
	}{
		{50, msec(50)},
		{90, msec(90)},
		{95, msec(100)},
		{99, msec(100)},
		{100, msec(100)},
		{0, msec(10)},
	} {
		if got := Percentile(sorted, tc.p); got != tc.want {
			t.Errorf("p%v = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestPercentileOfASingleValueIsThatValue(t *testing.T) {
	sorted := durations(42)
	for _, p := range []float64{0, 50, 99, 100} {
		if got := Percentile(sorted, p); got != msec(42) {
			t.Errorf("p%v = %v, want 42ms", p, got)
		}
	}
}

func TestPercentileOfNothingIsZero(t *testing.T) {
	if got := Percentile(nil, 95); got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestHistogramSplitsTheRangeIntoEqualBars(t *testing.T) {
	buckets := Histogram(durations(10, 20, 30, 40, 50), 5)

	if len(buckets) != 5 {
		t.Fatalf("got %d buckets, want 5", len(buckets))
	}
	total := 0
	for _, bucket := range buckets {
		total += bucket.Count
	}
	if total != 5 {
		t.Errorf("buckets hold %d values, want 5", total)
	}
	if buckets[len(buckets)-1].Upper != msec(50) {
		t.Errorf("last bucket ends at %v, want the slowest response", buckets[len(buckets)-1].Upper)
	}
}

func TestHistogramOfIdenticalValuesIsOneBar(t *testing.T) {
	buckets := Histogram(durations(7, 7, 7), 10)

	if len(buckets) != 1 || buckets[0].Count != 3 {
		t.Fatalf("got %+v, want one bar holding 3", buckets)
	}
}

func TestHistogramOfNothingIsNothing(t *testing.T) {
	if Histogram(nil, 10) != nil {
		t.Error("expected no buckets")
	}
	if Histogram(durations(1), 0) != nil {
		t.Error("expected no buckets for a zero bucket count")
	}
}

func TestSummarizeCountsSuccessesAndFailuresSeparately(t *testing.T) {
	attempts := []Attempt{
		{Latency: msec(10), Status: 200, Bytes: 100},
		{Latency: msec(20), Status: 204, Bytes: 0},
		{Latency: msec(30), Status: 500, Bytes: 50},
		{Latency: msec(40), Status: 404, Bytes: 10},
	}

	s := Summarize(attempts, time.Second)

	if s.Total != 4 {
		t.Errorf("total = %d, want 4", s.Total)
	}
	if s.Succeeded != 2 {
		t.Errorf("succeeded = %d, want 2 (only the 2xx)", s.Succeeded)
	}
	if s.Failed != 2 {
		t.Errorf("failed = %d, want 2", s.Failed)
	}
	if s.Bytes != 160 {
		t.Errorf("bytes = %d, want 160", s.Bytes)
	}
	if s.Statuses[500] != 1 || s.Statuses[200] != 1 {
		t.Errorf("statuses = %v", s.Statuses)
	}
}

func TestSummarizeGroupsErrorsByKind(t *testing.T) {
	attempts := []Attempt{
		{Err: context.DeadlineExceeded},
		{Err: context.DeadlineExceeded},
		{Err: &net.DNSError{Err: "no such host"}},
		{Latency: msec(5), Status: 200},
	}

	s := Summarize(attempts, time.Second)

	if s.Errors["timeout"] != 2 {
		t.Errorf("timeouts = %d, want 2", s.Errors["timeout"])
	}
	if s.Errors["dns"] != 1 {
		t.Errorf("dns = %d, want 1", s.Errors["dns"])
	}
	if s.Failed != 3 {
		t.Errorf("failed = %d, want 3", s.Failed)
	}
}

func TestSummarizeLeavesFailedRequestsOutOfTheLatencies(t *testing.T) {
	// A request that timed out after 10s must not drag the percentiles up:
	// it never produced a response time.
	attempts := []Attempt{
		{Latency: msec(10), Status: 200},
		{Latency: msec(20), Status: 200},
		{Latency: 10 * time.Second, Err: context.DeadlineExceeded},
	}

	s := Summarize(attempts, time.Second)

	if s.Latency.Max != msec(20) {
		t.Errorf("max = %v, want 20ms — the timeout must not count", s.Latency.Max)
	}
	if s.Latency.Mean != msec(15) {
		t.Errorf("mean = %v, want 15ms", s.Latency.Mean)
	}
}

func TestSummarizeMeasuresThroughputAgainstWallClock(t *testing.T) {
	attempts := make([]Attempt, 50)
	for i := range attempts {
		attempts[i] = Attempt{Latency: msec(1), Status: 200}
	}

	s := Summarize(attempts, 2*time.Second)

	if s.Throughput != 25 {
		t.Errorf("throughput = %v, want 25/s", s.Throughput)
	}
}

func TestSummarizeOfNothingIsEmptyRatherThanAPanic(t *testing.T) {
	s := Summarize(nil, time.Second)

	if s.Total != 0 || s.Latency.P99 != 0 || s.Histogram != nil {
		t.Errorf("got %+v, want an empty summary", s)
	}
}

func TestAttemptOKIsTrueOnlyForATwoHundred(t *testing.T) {
	cases := map[string]struct {
		attempt Attempt
		want    bool
	}{
		"200":      {Attempt{Status: 200}, true},
		"299":      {Attempt{Status: 299}, true},
		"301":      {Attempt{Status: 301}, false},
		"500":      {Attempt{Status: 500}, false},
		"error":    {Attempt{Status: 200, Err: errors.New("boom")}, false},
		"no reply": {Attempt{}, false},
	}
	for name, tc := range cases {
		if got := tc.attempt.OK(); got != tc.want {
			t.Errorf("%s: OK() = %v, want %v", name, got, tc.want)
		}
	}
}

func TestClassifyNamesTheCommonFailures(t *testing.T) {
	cases := map[string]error{
		"timeout":            context.DeadlineExceeded,
		"cancelled":          context.Canceled,
		"dns":                &net.DNSError{Err: "no such host"},
		"connection refused": errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
		"tls":                errors.New("x509: certificate signed by unknown authority"),
		"other":              errors.New("something new"),
	}
	for want, err := range cases {
		if got := classify(err); got != want {
			t.Errorf("classify(%v) = %q, want %q", err, got, want)
		}
	}
	if classify(nil) != "" {
		t.Error("a nil error should classify as empty")
	}
}
