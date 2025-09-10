package showq

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Showq struct {
	ageHistogram      *prometheus.HistogramVec
	sizeHistogram     *prometheus.HistogramVec
	queueMessageGauge *prometheus.GaugeVec
	knownQueues       map[string]struct{}
	constLabels       prometheus.Labels
	address           string
	network           string
	once              sync.Once
}

// ScanNullTerminatedEntries is a splitting function for bufio.Scanner
// to split entries by null bytes.
func ScanNullTerminatedEntries(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		// Valid record found.
		return i + 1, data[0:i], nil
	} else if atEOF && len(data) != 0 {
		// Data at the end of the file without a null terminator.
		return 0, nil, errors.New("expected null byte terminator")
	} else {
		// Request more data.
		return 0, nil, nil
	}
}

// CollectBinaryShowqFromReader parses Postfix's binary showq format.
func (s *Showq) collectBinaryShowqFromReader(file io.Reader, ch chan<- prometheus.Metric) error {
	err := s.collectBinaryShowqFromScanner(file)
	s.queueMessageGauge.Collect(ch)

	s.sizeHistogram.Collect(ch)
	s.ageHistogram.Collect(ch)
	return err
}

func (s *Showq) collectBinaryShowqFromScanner(file io.Reader) error {
	scanner := bufio.NewScanner(file)
	scanner.Split(ScanNullTerminatedEntries)
	queueSizes := make(map[string]float64)

	// HistogramVec is intended to capture data streams. Showq however always returns all emails
	// currently queued, therefore we need to reset the histograms before every collect.
	s.sizeHistogram.Reset()
	s.ageHistogram.Reset()

	now := float64(time.Now().UnixNano()) / 1e9
	queue := "unknown"
	for scanner.Scan() {
		// Parse a key/value entry.
		key := scanner.Text()
		if len(key) == 0 {
			// Empty key means a record separator.
			queue = "unknown"
			continue
		}
		if !scanner.Scan() {
			return fmt.Errorf("key %q does not have a value", key)
		}
		value := scanner.Text()

		switch key {
		case "queue_name":
			// The name of the message queue.
			queue = value
			queueSizes[queue]++
		case "size":
			// Message size in bytes.
			size, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			s.sizeHistogram.WithLabelValues(queue).Observe(size)
		case "time":
			// Message time as a UNIX timestamp.
			utime, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			s.ageHistogram.WithLabelValues(queue).Observe(now - utime)
		}
	}

	for q, count := range queueSizes {
		s.queueMessageGauge.WithLabelValues(q).Set(count)
	}
	for q := range s.knownQueues {
		if _, seen := queueSizes[q]; !seen {
			s.queueMessageGauge.WithLabelValues(q).Set(0)
			s.sizeHistogram.WithLabelValues(q)
			s.ageHistogram.WithLabelValues(q)
		}
	}
	return scanner.Err()
}

func (s *Showq) init() {
	s.once.Do(func() {
		s.ageHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "showq_message_age_seconds",
				Help:        "Age of messages in Postfix's message queue, in seconds",
				Buckets:     []float64{1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8},
				ConstLabels: s.constLabels,
			},
			[]string{"queue"})
		s.sizeHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "showq_message_size_bytes",
				Help:        "Size of messages in Postfix's message queue, in bytes",
				Buckets:     []float64{1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9},
				ConstLabels: s.constLabels,
			},
			[]string{"queue"})
		s.queueMessageGauge = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace:   "postfix",
				Name:        "showq_queue_depth",
				Help:        "Number of messages in Postfix's message queue",
				ConstLabels: s.constLabels,
			},
			[]string{"queue"},
		)
		s.knownQueues = map[string]struct{}{"active": {}, "deferred": {}, "hold": {}, "incoming": {}, "maildrop": {}}
	})
}

func (s *Showq) Collect(ch chan<- prometheus.Metric) error {
	fd, err := net.Dial(s.network, s.address)
	if err != nil {
		return err
	}
	defer fd.Close()
	s.init()
	return s.collectBinaryShowqFromReader(fd, ch)
}

func (s *Showq) Path() string {
	return fmt.Sprintf("%s://%s", s.network, s.address)
}

func (s *Showq) WithConstLabels(labels prometheus.Labels) *Showq {
	s.constLabels = labels
	return s
}

func (s *Showq) WithNetwork(network string) *Showq {
	s.network = network
	return s
}

func NewShowq(addr string) *Showq {
	return &Showq{
		address: addr,
		network: "unix",
	}
}
