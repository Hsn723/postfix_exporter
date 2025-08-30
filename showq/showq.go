package showq

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// "A07A81514      5156 Tue Feb 14 13:13:54  MAILER-DAEMON".
	messageLine = regexp.MustCompile(`^[0-9A-F]+([\*!]?) +(\d+) (\w{3} \w{3} +\d+ +\d+:\d{2}:\d{2}) +`)
)

type Showq struct {
	ageHistogram      *prometheus.HistogramVec
	sizeHistogram     *prometheus.HistogramVec
	queueMessageGauge *prometheus.GaugeVec
	knownQueues       map[string]struct{}
	readerFunc        func(io.Reader, chan<- prometheus.Metric) error
	path              string
	once              sync.Once
}

// CollectTextualShowqFromReader parses Postfix's textual showq output.
func (s *Showq) collectTextualShowqFromReader(file io.Reader, ch chan<- prometheus.Metric) error {
	err := s.collectTextualShowqFromScanner(file)

	s.queueMessageGauge.Collect(ch)
	s.sizeHistogram.Collect(ch)
	s.ageHistogram.Collect(ch)
	return err
}

func (s *Showq) collectTextualShowqFromScanner(file io.Reader) error {
	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanLines)
	queueSizes := make(map[string]float64)

	// Remove metrics from size and age histograms and re-initialize them. HistogramVec
	// is intended to capture data streams. Showq however always returns all emails
	// currently queued, therefore we need to reset the histograms before every collect.
	for _, q := range []string{"active", "hold", "other"} {
		// Delete histogram metrics
		s.sizeHistogram.DeleteLabelValues(q)
		s.ageHistogram.DeleteLabelValues(q)
		// Re-initialize histograms to ensure all labels are present.
		s.sizeHistogram.WithLabelValues(q)
		s.ageHistogram.WithLabelValues(q)
	}

	location, err := time.LoadLocation("Local")
	if err != nil {
		log.Println(err)
	}

	for scanner.Scan() {
		text := scanner.Text()
		matches := messageLine.FindStringSubmatch(text)
		if matches == nil {
			continue
		}
		queueMatch := matches[1]
		sizeMatch := matches[2]
		dateMatch := matches[3]

		// Derive the name of the message queue.
		var queue string
		switch queueMatch {
		case "*":
			queue = "active"
		case "!":
			queue = "hold"
		default:
			queue = "other"
		}

		// Parse the message size.
		size, err := strconv.ParseFloat(sizeMatch, 64)
		if err != nil {
			return err
		}

		// Parse the message date. Unfortunately, the
		// output contains no year number. Assume it
		// applies to the last year for which the
		// message date doesn't exceed time.Now().
		date, err := time.ParseInLocation("Mon Jan 2 15:04:05", dateMatch, location)
		if err != nil {
			return err
		}
		now := time.Now()
		date = date.AddDate(now.Year(), 0, 0)
		if date.After(now) {
			date = date.AddDate(-1, 0, 0)
		}

		queueSizes[queue]++
		s.sizeHistogram.WithLabelValues(queue).Observe(size)
		s.ageHistogram.WithLabelValues(queue).Observe(now.Sub(date).Seconds())
	}
	for q, count := range queueSizes {
		s.queueMessageGauge.WithLabelValues(q).Set(count)
		s.knownQueues[q] = struct{}{}
	}
	return scanner.Err()
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

	// Remove metrics from size and age histograms and re-initialize them. HistogramVec
	// is intended to capture data streams. Showq however always returns all emails
	// currently queued, therefore we need to reset the histograms before every collect.
	for _, q := range []string{"active", "deferred", "hold", "incoming", "maildrop"} {
		// Delete histogram metrics
		s.sizeHistogram.DeleteLabelValues(q)
		s.ageHistogram.DeleteLabelValues(q)
		// Re-initialize histograms to ensure all labels are present.
		s.sizeHistogram.WithLabelValues(q)
		s.ageHistogram.WithLabelValues(q)
	}

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
		s.knownQueues[q] = struct{}{}
	}
	for q := range s.knownQueues {
		if _, seen := queueSizes[q]; !seen {
			s.queueMessageGauge.WithLabelValues(q).Set(0)
		}
	}
	return scanner.Err()
}

func (s *Showq) init(file io.Reader) {
	s.once.Do(func() {
		s.ageHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "postfix",
				Name:      "showq_message_age_seconds",
				Help:      "Age of messages in Postfix's message queue, in seconds",
				Buckets:   []float64{1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8},
			},
			[]string{"queue"})
		s.sizeHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "postfix",
				Name:      "showq_message_size_bytes",
				Help:      "Size of messages in Postfix's message queue, in bytes",
				Buckets:   []float64{1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9},
			},
			[]string{"queue"})
		s.queueMessageGauge = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "postfix",
				Name:      "showq_queue_depth",
				Help:      "Number of messages in Postfix's message queue",
			},
			[]string{"queue"},
		)

		s.knownQueues = make(map[string]struct{})

		reader := bufio.NewReader(file)
		buf, err := reader.Peek(128)
		if err != nil && err != io.EOF {
			log.Printf("Could not read postfix output, %v", err)
		}
		// The output format of Postfix's 'showq' command depends on the version
		// used. Postfix 2.x uses a textual format, identical to the output of
		// the 'mailq' command. Postfix 3.x uses a binary format, where entries
		// are terminated using null bytes. Auto-detect the format by scanning
		// for null bytes in the first 128 bytes of output.
		if bytes.IndexByte(buf, 0) >= 0 {
			s.readerFunc = s.collectBinaryShowqFromReader
		} else {
			s.readerFunc = s.collectTextualShowqFromReader
		}
	})
}

func (s *Showq) Collect(ch chan<- prometheus.Metric) error {
	fd, err := net.Dial("unix", s.path)
	if err != nil {
		return err
	}
	defer fd.Close()
	s.init(fd)
	return s.readerFunc(fd, ch)
}

func NewShowq(path string) *Showq {
	return &Showq{
		path: path,
	}
}
