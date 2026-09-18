// Package report turns a run's raw attempts into the numbers a person reads.
//
// Everything here is a pure function over a slice of attempts: no network, no
// clock, no goroutines. That is what lets the percentile arithmetic be tested
// against hand-worked examples rather than against whatever the last run happened
// to produce.
package report

import (
	"math"
	"sort"
	"time"
)

// Attempt is one request that finished, successfully or not.
type Attempt struct {
	Latency time.Duration
	Status  int
	Bytes   int64
	Err     error
}

// OK reports whether the attempt counts as a success: it completed and the
// server answered 2xx. A 500 that arrived quickly is still a failure.
func (a Attempt) OK() bool {
	return a.Err == nil && a.Status >= 200 && a.Status < 300
}

// Latencies holds the distribution of response times.
type Latencies struct {
	Min  time.Duration `json:"min"`
	Max  time.Duration `json:"max"`
	Mean time.Duration `json:"mean"`
	P50  time.Duration `json:"p50"`
	P90  time.Duration `json:"p90"`
	P95  time.Duration `json:"p95"`
	P99  time.Duration `json:"p99"`
}

// Bucket is one bar of the latency histogram.
type Bucket struct {
	Upper time.Duration `json:"upper"`
	Count int           `json:"count"`
}

// Summary is the whole result of a run.
type Summary struct {
	Total      int            `json:"total"`
	Succeeded  int            `json:"succeeded"`
	Failed     int            `json:"failed"`
	Elapsed    time.Duration  `json:"elapsed"`
	Throughput float64        `json:"throughput_per_second"`
	Bytes      int64          `json:"bytes"`
	Statuses   map[int]int    `json:"statuses"`
	Errors     map[string]int `json:"errors"`
	Latency    Latencies      `json:"latency"`
	Histogram  []Bucket       `json:"histogram"`
}

// Percentile returns the p-th percentile of sorted, using the nearest-rank
// method: the smallest value at or below which at least p% of the data sits.
//
// Nearest rank rather than interpolation, because a latency percentile should
// always be a latency that actually happened.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// Histogram splits the latencies into `buckets` equal-width bars between the
// fastest and the slowest response.
func Histogram(sorted []time.Duration, buckets int) []Bucket {
	if len(sorted) == 0 || buckets < 1 {
		return nil
	}

	lo, hi := sorted[0], sorted[len(sorted)-1]
	if hi == lo {
		// Every response took the same time; one bar is the honest picture.
		return []Bucket{{Upper: hi, Count: len(sorted)}}
	}

	width := float64(hi-lo) / float64(buckets)
	out := make([]Bucket, buckets)
	for i := range out {
		out[i].Upper = lo + time.Duration(width*float64(i+1))
	}
	out[buckets-1].Upper = hi

	index := 0
	for _, value := range sorted {
		for index < buckets-1 && value > out[index].Upper {
			index++
		}
		out[index].Count++
	}
	return out
}

// Summarize folds the attempts into a Summary. elapsed is the wall-clock time
// the run took, which is what throughput is measured against.
func Summarize(attempts []Attempt, elapsed time.Duration) Summary {
	summary := Summary{
		Total:    len(attempts),
		Elapsed:  elapsed,
		Statuses: map[int]int{},
		Errors:   map[string]int{},
	}

	latencies := make([]time.Duration, 0, len(attempts))
	var total time.Duration

	for _, attempt := range attempts {
		summary.Bytes += attempt.Bytes

		if attempt.Err != nil {
			summary.Failed++
			summary.Errors[classify(attempt.Err)]++
			continue
		}

		summary.Statuses[attempt.Status]++
		if attempt.OK() {
			summary.Succeeded++
		} else {
			summary.Failed++
		}

		// Only requests that got an answer have a meaningful latency: a
		// connection that timed out would otherwise drag the percentiles toward
		// the timeout value and hide how fast the real responses were.
		latencies = append(latencies, attempt.Latency)
		total += attempt.Latency
	}

	if elapsed > 0 {
		summary.Throughput = float64(summary.Total) / elapsed.Seconds()
	}

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		summary.Latency = Latencies{
			Min:  latencies[0],
			Max:  latencies[len(latencies)-1],
			Mean: total / time.Duration(len(latencies)),
			P50:  Percentile(latencies, 50),
			P90:  Percentile(latencies, 90),
			P95:  Percentile(latencies, 95),
			P99:  Percentile(latencies, 99),
		}
		summary.Histogram = Histogram(latencies, 10)
	}

	return summary
}
