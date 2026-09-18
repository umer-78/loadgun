# loadgun

An HTTP load testing tool in Go. Point it at a URL, and it tells you how fast the
answers came back, how many of them were wrong, and what the slow tail looks like.

```
$ loadgun -n 500 -c 25 --keepalive http://localhost:8123/
GET http://localhost:8123/ — 25 workers, 500 requests
Requests    500 in 110.025ms (4544.4/s)
Succeeded   500 (100.0%)
Failed      0
Received    15.6 KB

Latency     min 1.203ms   mean 5.167ms   max 11.869ms
            p50 5.2ms   p90 8.433ms   p95 8.878ms   p99 9.96ms

Distribution
  <= 2.27ms       74  ########################################
  <= 3.336ms      69  #####################################
  <= 4.403ms      68  ####################################
  <= 5.47ms       74  ########################################
  <= 6.536ms      57  ##############################
  <= 7.603ms      65  ###################################
  <= 8.669ms      59  ###############################
  <= 9.736ms      28  ###############
  <= 10.803ms      4  ##
  <= 11.869ms      2  #

Status codes
  200  500
```

- **Single static binary**, no dependencies outside the standard library
- **48 tests, 88% coverage**, all passing under `-race`
- Exits non-zero when anything failed, so it works as a CI gate
- `table`, `json` or `csv` output

## Install

```bash
go install github.com/umer-78/loadgun/cmd/loadgun@latest
```

Or from a clone:

```bash
git clone https://github.com/umer-78/loadgun.git
cd loadgun
make build          # ./bin/loadgun
make race           # tests under the race detector
```

Or with Docker (distroless, runs as a non-root user):

```bash
docker build -t loadgun .
docker run --rm loadgun -n 200 -c 20 https://example.com
```

## Usage

```
loadgun [options] <url>

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
```

```bash
# 500 requests, 25 at a time
loadgun -n 500 -c 25 http://localhost:8080/health

# hold 50 requests a second for 30 seconds
loadgun -d 30s -c 20 --rate 50 https://example.com/api

# a POST with a body and a header, as JSON for a dashboard
loadgun -n 1000 -c 50 -m POST -H 'Content-Type: application/json' \
        -b '{"id":1}' --format json -o report.json https://example.com/api

# every individual response time, for your own analysis
loadgun -n 1000 -c 20 --format csv -o timings.csv https://example.com
```

## As a CI gate

The exit code is 1 if any request failed, so a smoke test is one line:

```yaml
- name: The service holds up
  run: loadgun -n 200 -c 20 --rate 100 http://localhost:8080/health
```

```
$ loadgun -n 200 -c 20 --keepalive http://localhost:8123/flaky
Requests    200 in 124.972ms (1600.4/s)
Succeeded   183 (91.5%)
Failed      17

Status codes
  200  183
  503  17
$ echo $?
1
```

## Four decisions that decide whether the numbers mean anything

**Percentiles are nearest-rank, not interpolated.** p95 is a latency that a
request actually experienced, not the average of two that never happened. Ten
requests from 10ms to 100ms give p50 = 50ms and p95 = 100ms, and there is a test
that says so in exactly those numbers.

**A failed request has no latency.** A request that times out after 10 seconds is
counted as a failure — and left out of the latency distribution entirely. Folding
it in would put a 10-second spike in the tail and hide how fast the responses
that *arrived* actually were.

**The duration limit stops new requests; it does not cut off the ones in
flight.** Cancelling a request at the deadline would record it as a failure of
the target, when all that happened is that the test ended. The last few requests
get to finish. Before this was fixed, a 3-second run at 50/s reported one failure
every time — the request that happened to be open when the clock ran out.

**The response body is drained before the clock stops.** A response is not
received until its body is. Draining is also what lets a keep-alive connection be
reused instead of being thrown away, which is why `--keepalive` measures the
server rather than the TCP handshake.

## How it works

```
cmd/loadgun          the binary and signal handling
internal/cli         flags, output selection, exit codes
internal/load        the worker pool that sends the requests
internal/report      percentiles, histogram, text/JSON/CSV rendering
```

The runner is deliberately boring: a fixed pool of goroutines, one channel handing
out work, one channel collecting results. Nothing is shared and incremented across
goroutines, so there is no counter to race on and the totals cannot drift. The
rate cap is a `time.Ticker` the dispatcher waits on, so it applies across all the
workers rather than per worker.

`internal/report` is pure functions over a slice of attempts — no clock, no
network, no goroutines — which is what makes the arithmetic testable against
hand-worked examples rather than against whatever the last run produced.

Ctrl-C stops the run and still prints the report. An interrupted load test that
throws away its measurements is worse than useless.

## Tests

```
$ go test -race -cover ./...
ok  github.com/umer-78/loadgun/internal/cli       coverage: 85.7% of statements
ok  github.com/umer-78/loadgun/internal/load      coverage: 95.2% of statements
ok  github.com/umer-78/loadgun/internal/report    coverage: 90.5% of statements
```

They run against `httptest` servers, so they need no network and no fixtures.
The ones worth naming:

- the server is asked how many requests it saw, and how many it was serving at
  once — the concurrency cap is verified from the far end, not from the tool's
  own bookkeeping
- 20 requests at 100/s cannot finish in under ~190ms, whatever the worker count
- a redirect is *not* followed, because following one would measure two requests
  as one
- a 503 is recorded with its latency and still counted as a failure
- cancelling mid-run keeps what was already measured and discards the requests
  that were cut off
- percentiles, the histogram, error classification and every output format are
  each checked against worked examples

## Limits

One URL per run, no scenario scripting, no HTTP/2-specific tuning, no distributed
mode. It measures what a client sees from one machine. For anything larger you
want a purpose-built rig — but for "is this endpoint fast enough, and does it stay
fast under twenty clients", this is the whole job.

## Licence

MIT — see [LICENSE](LICENSE).
