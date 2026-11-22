// Package servicejournal keeps a private, bounded tail of the daemon's logs.
package servicejournal

import (
	"bytes"
	"strings"
	"sync"
)

const maxBytes = 512 << 10
const maxLines = 100

type Journal struct {
	mu        sync.Mutex
	lines     [maxLines]string
	start     int
	count     int
	bytes     int
	truncated bool
}

// Write receives complete records from slog's text handler.
func (j *Journal) Write(p []byte) (int, error) {
	n := len(p)
	j.mu.Lock()
	defer j.mu.Unlock()
	if n >= maxBytes {
		p = p[:maxBytes-1]
		j.truncated = true
	}
	line := string(bytes.TrimRight(p, "\r\n"))
	for j.count == maxLines || j.bytes+len(line)+1 > maxBytes && j.count > 0 {
		j.bytes -= len(j.lines[j.start]) + 1
		j.lines[j.start] = ""
		j.start = (j.start + 1) % maxLines
		j.count--
		j.truncated = true
	}
	j.lines[(j.start+j.count)%maxLines] = line
	j.count++
	j.bytes += len(line) + 1
	return n, nil
}

func (j *Journal) Tail() (text string, truncated bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var result strings.Builder
	result.Grow(j.bytes)
	for i := j.count - 1; i >= 0; i-- {
		if i != j.count-1 {
			result.WriteByte('\n')
		}
		result.WriteString(j.lines[(j.start+i)%maxLines])
	}
	return result.String(), j.truncated
}
