package backup

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (r *repository) check(ctx context.Context, allData bool) error {
	return r.checkWithProgress(ctx, allData, nil)
}

func (r *repository) checkWithProgress(ctx context.Context, allData bool, progress func(float64)) error {
	args := []string{"check"}
	if allData {
		args = append(args, "--read-data")
	}
	cmd := r.command(ctx, nil, args...)
	cmd.Env = append(cmd.Env, "RESTIC_PROGRESS_FPS=1")
	var stderr boundedOutput
	stderr.max = 8192
	stdout := checkOutput{output: boundedOutput{max: 4096}, progress: progress}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic check: %w\n%s", err, strings.TrimSpace(stderr.String()+"\n"+stdout.output.String()))
	}
	return nil
}

type checkOutput struct {
	output   boundedOutput
	progress func(float64)
	line     []byte
}

func (o *checkOutput) Write(p []byte) (int, error) {
	_, _ = o.output.Write(p)
	if o.progress != nil {
		for rest := p; len(rest) > 0; {
			line, tail, ended := bytes.Cut(rest, []byte{'\n'})
			o.line = append(o.line, line[:min(len(line), 512-len(o.line))]...)
			if !ended {
				break
			}
			o.reportPackProgress()
			o.line = o.line[:0]
			rest = tail
		}
	}
	return len(p), nil
}

func (o *checkOutput) reportPackProgress() {
	// Example from Restic 0.18: [0:03] 42.00%  21 / 50 packs.
	fields := strings.Fields(string(o.line))
	if len(fields) != 6 || fields[3] != "/" || fields[5] != "packs" || !strings.HasPrefix(fields[0], "[") {
		return
	}
	done, doneErr := strconv.ParseUint(fields[2], 10, 64)
	total, totalErr := strconv.ParseUint(fields[4], 10, 64)
	if doneErr == nil && totalErr == nil && total > 0 && done <= total {
		o.progress(float64(done) / float64(total))
	}
}
