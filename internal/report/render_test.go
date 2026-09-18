package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func sample() Summary {
	return Summarize([]Attempt{
		{Latency: 10 * time.Millisecond, Status: 200, Bytes: 1024},
		{Latency: 20 * time.Millisecond, Status: 200, Bytes: 1024},
		{Latency: 30 * time.Millisecond, Status: 503, Bytes: 12},
		{Err: errors.New("dial tcp: connect: connection refused")},
	}, 2*time.Second)
}

func TestTextShowsTheHeadlineNumbers(t *testing.T) {
	text := Text(sample())

	for _, want := range []string{
		"Requests    4",
		"Succeeded   2 (50.0%)",
		"Failed      2",
		"p50", "p99",
		"Distribution",
		"Status codes",
		"503",
		"Errors",
		"connection refused",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report is missing %q:\n%s", want, text)
		}
	}
}

func TestTextOfAnEmptySummaryDoesNotPanic(t *testing.T) {
	text := Text(Summarize(nil, 0))

	if !strings.Contains(text, "Requests    0") {
		t.Errorf("got:\n%s", text)
	}
}

func TestJSONCarriesMillisecondsAsWellAsRawDurations(t *testing.T) {
	encoded, err := JSON(sample())
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Total     int                `json:"total"`
		Succeeded int                `json:"succeeded"`
		LatencyMs map[string]float64 `json:"latency_ms"`
		Statuses  map[string]int     `json:"statuses"`
	}
	if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, encoded)
	}

	if parsed.Total != 4 || parsed.Succeeded != 2 {
		t.Errorf("total=%d succeeded=%d", parsed.Total, parsed.Succeeded)
	}
	if parsed.LatencyMs["p50"] != 20 {
		t.Errorf("p50 = %v ms, want 20", parsed.LatencyMs["p50"])
	}
	if parsed.Statuses["503"] != 1 {
		t.Errorf("statuses = %v", parsed.Statuses)
	}
}

func TestCSVHasAHeaderAndOneRowPerAttempt(t *testing.T) {
	var buffer bytes.Buffer

	attempts := []Attempt{
		{Latency: 1500 * time.Microsecond, Status: 200, Bytes: 10},
		{Err: errors.New("dial tcp: connect: connection refused")},
	}
	if err := CSV(&buffer, attempts); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want header + 2:\n%s", len(lines), buffer.String())
	}
	if lines[0] != "n,latency_ms,status,bytes,error" {
		t.Errorf("header = %q", lines[0])
	}
	if lines[1] != "1,1.500,200,10," {
		t.Errorf("first row = %q", lines[1])
	}
	if !strings.HasSuffix(lines[2], "connection refused") {
		t.Errorf("second row = %q", lines[2])
	}
}

func TestDurationsAreRoundedToSomethingReadable(t *testing.T) {
	cases := map[time.Duration]string{
		0:                                    "0s",
		1234567 * time.Nanosecond:            "1.235ms",
		1500 * time.Millisecond:              "1.5s",
		999 * time.Nanosecond:                "999ns",
		2*time.Second + 400*time.Microsecond: "2s",
	}
	for input, want := range cases {
		if got := round(input); got != want {
			t.Errorf("round(%v) = %q, want %q", input, got, want)
		}
	}
}

func TestBytesAreScaledToASensibleUnit(t *testing.T) {
	cases := map[int64]string{
		512:             "512 B",
		2048:            "2.0 KB",
		5 * 1024 * 1024: "5.0 MB",
	}
	for input, want := range cases {
		if got := humanBytes(input); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", input, got, want)
		}
	}
}
