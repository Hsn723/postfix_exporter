package logsource

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/alecthomas/kingpin/v2"
	"github.com/nxadm/tail"
	"gopkg.in/tomb.v1"
)

var defaultConfig = tail.Config{
	ReOpen:    true,                               // reopen the file if it's rotated
	MustExist: true,                               // fail immediately if the file is missing or has incorrect permissions
	Follow:    true,                               // run in follow mode
	Poll:      false,                              // poll for file changes instead of using inotify
	Location:  &tail.SeekInfo{Whence: io.SeekEnd}, // seek to end of file
	Logger:    tail.DiscardingLogger,
}

// A FileLogSource can read lines from a file.
type FileLogSource struct {
	tailer    *tail.Tail
	unhealthy bool
	LogSourceDefaults
}

// NewFileLogSource creates a new log source, tailing the given file.
func NewFileLogSource(path string, config tail.Config) (*FileLogSource, error) {
	tailer, err := tail.TailFile(path, config)
	if err != nil {
		return nil, err
	}
	return &FileLogSource{tailer: tailer}, nil
}

func (s *FileLogSource) Close() error {
	go func() {
		// Stop() waits for the tailer goroutine to shut down, but it
		// can be blocking on sending on the Lines channel...
		for range s.tailer.Lines {
		}
	}()
	// Do not call .CleanUp() if the file should be tailed again
	// see also https://pkg.go.dev/github.com/nxadm/tail@v1.4.11#Tail.Cleanup
	return s.tailer.Stop()
}

func (s *FileLogSource) Path() string {
	return s.tailer.Filename
}

func (s *FileLogSource) Read(ctx context.Context) (string, error) {
	select {
	case line, ok := <-s.tailer.Lines:
		if !ok {
			s.unhealthy = true
			if tailErr := s.tailer.Tomb.Err(); tailErr != nil && !errors.Is(tailErr, tomb.ErrStillAlive) {
				return "", tailErr
			}
			return "", io.EOF
		}
		return line.Text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// A fileLogSourceFactory is a factory that can create log sources
// from command line flags.
//
// Because this factory is enabled by default, it must always be
// registered last.
type fileLogSourceFactory struct {
	LogSourceFactoryDefaults
	path      string
	mustExist bool
	debug     bool
	poll      bool
	source    *FileLogSource
}

func (f *fileLogSourceFactory) Init(app *kingpin.Application) {
	app.Flag("postfix.logfile_path", "Path where Postfix writes log entries.").Default("/var/log/mail.log").StringVar(&f.path)
	app.Flag("postfix.logfile_must_exist", "Fail if the log file doesn't exist.").Default("true").BoolVar(&f.mustExist)
	app.Flag("postfix.logfile_debug", "Enable debug logging for the log file.").Default("false").BoolVar(&f.debug)
	app.Flag("postfix.logfile_poll", "Poll for file changes instead of using inotify.").Default("false").BoolVar(&f.poll)
}

// config returns a tail.Config configured from the factory's fields.
func (f fileLogSourceFactory) config() tail.Config {
	conf := defaultConfig
	conf.MustExist = f.mustExist
	conf.Poll = f.poll
	if f.debug {
		conf.Logger = tail.DefaultLogger
	}
	return conf
}

func (f *fileLogSourceFactory) New(ctx context.Context) ([]LogSourceCloser, error) {
	if f.path == "" {
		return nil, nil
	}
	slog.Info("Reading log events from file", "path", f.path)
	logSource, err := NewFileLogSource(f.path, f.config())
	if err != nil {
		return nil, err
	}
	f.source = logSource
	return []LogSourceCloser{logSource}, nil
}

func (f *fileLogSourceFactory) Watchdog(ctx context.Context) bool {
	if f.source == nil {
		return false
	}
	return f.source.unhealthy
}
