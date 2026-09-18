package report

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Text renders the summary the way it appears in a terminal.
func Text(s Summary) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Requests    %d in %s (%.1f/s)\n", s.Total, round(s.Elapsed), s.Throughput)
	fmt.Fprintf(&b, "Succeeded   %d (%.1f%%)\n", s.Succeeded, percent(s.Succeeded, s.Total))
	fmt.Fprintf(&b, "Failed      %d\n", s.Failed)
	fmt.Fprintf(&b, "Received    %s\n\n", humanBytes(s.Bytes))

	fmt.Fprintf(&b, "Latency     min %s   mean %s   max %s\n",
		round(s.Latency.Min), round(s.Latency.Mean), round(s.Latency.Max))
	fmt.Fprintf(&b, "            p50 %s   p90 %s   p95 %s   p99 %s\n\n",
		round(s.Latency.P50), round(s.Latency.P90), round(s.Latency.P95), round(s.Latency.P99))

	if len(s.Histogram) > 0 {
		b.WriteString("Distribution\n")
		widest := 0
		for _, bucket := range s.Histogram {
			if bucket.Count > widest {
				widest = bucket.Count
			}
		}
		for _, bucket := range s.Histogram {
			bar := 0
			if widest > 0 {
				bar = bucket.Count * 40 / widest
			}
			fmt.Fprintf(&b, "  <= %-9s %5d  %s\n", round(bucket.Upper), bucket.Count, strings.Repeat("#", bar))
		}
		b.WriteString("\n")
	}

	if len(s.Statuses) > 0 {
		b.WriteString("Status codes\n")
		for _, code := range sortedKeys(s.Statuses) {
			fmt.Fprintf(&b, "  %d  %d\n", code, s.Statuses[code])
		}
	}

	if len(s.Errors) > 0 {
		b.WriteString("Errors\n")
		names := make([]string, 0, len(s.Errors))
		for name := range s.Errors {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&b, "  %-20s %d\n", name, s.Errors[name])
		}
	}

	return b.String()
}

// JSON renders the summary for a pipeline to read.
func JSON(s Summary) (string, error) {
	// Durations are nanosecond integers in Go's JSON; milliseconds are what a
	// person or a dashboard actually wants.
	type out struct {
		Summary
		LatencyMs map[string]float64 `json:"latency_ms"`
	}
	payload := out{
		Summary: s,
		LatencyMs: map[string]float64{
			"min":  ms(s.Latency.Min),
			"mean": ms(s.Latency.Mean),
			"max":  ms(s.Latency.Max),
			"p50":  ms(s.Latency.P50),
			"p90":  ms(s.Latency.P90),
			"p95":  ms(s.Latency.P95),
			"p99":  ms(s.Latency.P99),
		},
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// CSV writes one row per attempt, for people who want to do their own maths.
func CSV(w io.Writer, attempts []Attempt) error {
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"n", "latency_ms", "status", "bytes", "error"}); err != nil {
		return err
	}
	for i, attempt := range attempts {
		message := ""
		if attempt.Err != nil {
			message = classify(attempt.Err)
		}
		row := []string{
			strconv.Itoa(i + 1),
			strconv.FormatFloat(ms(attempt.Latency), 'f', 3, 64),
			strconv.Itoa(attempt.Status),
			strconv.FormatInt(attempt.Bytes, 10),
			message,
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func percent(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

// round trims a duration to something readable: 1.234ms, not 1.234567ms.
func round(d time.Duration) string {
	switch {
	case d == 0:
		return "0s"
	case d < time.Microsecond:
		return d.String()
	case d < time.Millisecond:
		return d.Round(10 * time.Nanosecond).String()
	case d < time.Second:
		return d.Round(time.Microsecond).String()
	default:
		return d.Round(time.Millisecond).String()
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func sortedKeys(m map[int]int) []int {
	keys := make([]int, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}
