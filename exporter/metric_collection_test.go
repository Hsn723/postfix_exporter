package exporter

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/hsn723/postfix_exporter/logsource"
	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
)

// metricValue reads a gauge or a counter.
func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var d io_prometheus_client.Metric
	if err := m.Write(&d); err != nil {
		t.Fatal(err)
	}
	if d.Gauge != nil {
		return d.Gauge.GetValue()
	}
	return d.Counter.GetValue()
}

type readResult struct {
	err  error
	line string
}

// scriptedLogSource returns the scripted results in order, then blocks until the
// context is cancelled.
type scriptedLogSource struct {
	logsource.LogSourceDefaults
	results []readResult
	mu      sync.Mutex
	reads   int
}

func (s *scriptedLogSource) Path() string { return "scripted" }

func (s *scriptedLogSource) Read(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.reads < len(s.results) {
		r := s.results[s.reads]
		s.reads++
		s.mu.Unlock()
		return r.line, r.err
	}
	s.mu.Unlock()
	<-ctx.Done()
	return "", ctx.Err()
}

func (s *scriptedLogSource) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func setRetryDelays(t *testing.T, minDelay, maxDelay time.Duration) {
	t.Helper()
	oldMin, oldMax := logReadRetryMin, logReadRetryMax
	logReadRetryMin, logReadRetryMax = minDelay, maxDelay
	t.Cleanup(func() { logReadRetryMin, logReadRetryMax = oldMin, oldMax })
}

const removedLine = "Feb 11 16:49:24 letterman postfix/qmgr[8204]: AAB4D259B1: removed"

func TestStartMetricCollectionRetriesTransientErrors(t *testing.T) {
	setRetryDelays(t, time.Millisecond, 4*time.Millisecond)
	transient := errors.New("failed to initialize log stream: 502 Bad Gateway")
	src := &scriptedLogSource{results: []readResult{
		{err: transient},
		{err: transient},
		{err: transient},
		{line: removedLine},
	}}
	e := NewPostfixExporter(nil, src, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.StartMetricCollection(ctx)
		close(done)
	}()

	assert.Eventually(t, func() bool {
		return metricValue(t, e.qmgrRemoves) == 1
	}, 5*time.Second, time.Millisecond, "collection should resume after transient errors")
	assert.Equal(t, 1.0, metricValue(t, e.postfixUp.WithLabelValues("scripted")))

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collection did not stop on cancellation")
	}
	assert.Equal(t, 0.0, metricValue(t, e.postfixUp.WithLabelValues("scripted")))
}

func TestStartMetricCollectionReportsDownWhileRetrying(t *testing.T) {
	setRetryDelays(t, time.Hour, time.Hour)
	src := &scriptedLogSource{results: []readResult{
		{line: removedLine},
		{err: errors.New("stream lost")},
	}}
	e := NewPostfixExporter(nil, src, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.StartMetricCollection(ctx)

	assert.Eventually(t, func() bool { return src.readCount() == 2 }, 5*time.Second, time.Millisecond)
	assert.Eventually(t, func() bool {
		return metricValue(t, e.postfixUp.WithLabelValues("scripted")) == 0
	}, 5*time.Second, time.Millisecond, "postfix_up should be 0 while the log source fails")
}

func TestStartMetricCollectionStopsAtEOF(t *testing.T) {
	setRetryDelays(t, time.Millisecond, 4*time.Millisecond)
	src := &scriptedLogSource{results: []readResult{
		{line: removedLine},
		{err: io.EOF},
		{line: removedLine},
	}}
	e := NewPostfixExporter(nil, src, false)
	done := make(chan struct{})
	go func() {
		e.StartMetricCollection(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collection should stop at the end of the log")
	}
	assert.Equal(t, 2, src.readCount())
	assert.Equal(t, 1.0, metricValue(t, e.qmgrRemoves))
	assert.Equal(t, 0.0, metricValue(t, e.postfixUp.WithLabelValues("scripted")))
}
