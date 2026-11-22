package backup

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultConnectTimeout = 15 * time.Second
	defaultIdleTimeout    = 5 * time.Minute
	operationTimeoutMin   = time.Second
)

type operationTimeouts struct {
	connect time.Duration
	idle    time.Duration
}

func readOperationTimeouts() (operationTimeouts, error) {
	options := operationTimeouts{defaultConnectTimeout, defaultIdleTimeout}
	for name, target := range map[string]*time.Duration{
		"BACKUPHUB_CONNECT_TIMEOUT": &options.connect,
		"BACKUPHUB_IDLE_TIMEOUT":    &options.idle,
	} {
		if value := os.Getenv(name); value != "" {
			duration, err := time.ParseDuration(value)
			if err != nil || duration < operationTimeoutMin || duration > 24*time.Hour {
				return options, fmt.Errorf("%s must be a duration from 1s to 24h", name)
			}
			*target = duration
		}
	}
	return options, nil
}

type operationOptionsKey struct{}
type serviceLeaseKey struct{}
type progressKey struct{}

func connectTimeout(ctx context.Context) time.Duration {
	if options, ok := ctx.Value(operationOptionsKey{}).(operationTimeouts); ok {
		return options.connect
	}
	return defaultConnectTimeout
}

func connectSeconds(ctx context.Context) string {
	return strconv.FormatInt(int64((connectTimeout(ctx)+time.Second-1)/time.Second), 10)
}

func watchProgress(parent context.Context) (context.Context, func()) {
	idle := defaultIdleTimeout
	if options, ok := parent.Value(operationOptionsKey{}).(operationTimeouts); ok {
		idle = options.idle
	}
	ctx, cancel := context.WithCancelCause(parent)
	started := time.Now()
	var last atomic.Int64
	beat := func() { last.Store(int64(time.Since(started))) }
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				remaining := idle - (time.Since(started) - time.Duration(last.Load()))
				if remaining <= 0 {
					cancel(context.DeadlineExceeded)
					return
				}
				timer.Reset(remaining)
			}
		}
	}()
	return context.WithValue(ctx, progressKey{}, beat), func() {
		cancel(context.Canceled)
		<-stopped
	}
}

func noteProgress(ctx context.Context) {
	if beat, ok := ctx.Value(progressKey{}).(func()); ok {
		beat()
	}
}

type progressReader struct {
	ctx context.Context
	io.Reader
}

func (r progressReader) Read(b []byte) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.Reader.Read(b)
	if n > 0 {
		noteProgress(r.ctx)
	}
	return n, err
}

type progressWriter struct {
	ctx context.Context
	io.Writer
}

func (w progressWriter) Write(b []byte) (int, error) {
	if err := context.Cause(w.ctx); err != nil {
		return 0, err
	}
	n, err := w.Writer.Write(b)
	if n > 0 {
		noteProgress(w.ctx)
	}
	return n, err
}

// Children inherit the service lease. Even after SIGKILL, a second process
// cannot recover or dispatch until the kernel has closed the old children's
// descriptors. Extra descriptors such as the SMB1 password keep their indices.
func operationCommand(ctx context.Context, files []*os.File, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.WaitDelay = 5 * time.Second
	cmd.ExtraFiles = files
	if lease, ok := ctx.Value(serviceLeaseKey{}).(*os.File); ok {
		cmd.ExtraFiles = append(cmd.ExtraFiles, lease)
	}
	return cmd
}

func watchFileProgress(ctx context.Context, path string) func() {
	stopped, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		var size int64
		for {
			select {
			case <-stopped:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if info, err := os.Stat(path); err == nil && info.Size() > size {
					size = info.Size()
					noteProgress(ctx)
				}
			}
		}
	}()
	return func() {
		close(stopped)
		<-done
	}
}

type progressConn struct {
	net.Conn
	ctx context.Context
}

func (c progressConn) Read(b []byte) (int, error) {
	return (progressReader{c.ctx, c.Conn}).Read(b)
}
