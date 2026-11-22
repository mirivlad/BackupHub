package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSnapshotIndexLargeTree(t *testing.T) {
	count := 100000
	if raw := os.Getenv("BACKUPHUB_LARGE_TREE"); raw != "" {
		var err error
		count, err = strconv.Atoi(raw)
		if err != nil || count < 1 {
			t.Fatal("invalid BACKUPHUB_LARGE_TREE")
		}
	}
	repo := &repository{passwordFile: t.TempDir() + "/restic.key"}
	runtime.GC()
	stop := make(chan struct{})
	peak := make(chan uint64, 1)
	go func() {
		var high uint64
		var stats runtime.MemStats
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			runtime.ReadMemStats(&stats)
			high = max(high, stats.HeapAlloc)
			select {
			case <-ticker.C:
			case <-stop:
				peak <- high
				return
			}
		}
	}()
	start := time.Now()
	err := repo.withSnapshotIndex(context.Background(), func(index *snapshotIndex) error {
		ctx := context.Background()
		for n := 0; n < count; n++ {
			if err := index.add(ctx, largeTreePath(n), snapshotArchiveEntry{kind: "file", size: 1}); err != nil {
				return err
			}
		}
		reader, writer := io.Pipe()
		defer reader.Close()
		go func() {
			archive := tar.NewWriter(writer)
			var err error
			for n := count - 1; n >= 0 && err == nil; n-- {
				err = archive.WriteHeader(&tar.Header{Name: largeTreePath(n)[1:], Mode: 0600, Size: 1})
				if err == nil {
					_, err = archive.Write([]byte{'x'})
				}
			}
			_ = writer.CloseWithError(errors.Join(err, archive.Close()))
		}()
		return index.verifyArchive(ctx, repo, "", reader)
	})
	close(stop)
	heap := <-peak
	var usage unix.Rusage
	_ = unix.Getrusage(unix.RUSAGE_SELF, &usage)
	t.Logf("%d entries, sampled peak Go heap %.1f MiB, process peak RSS %.1f MiB, elapsed %s",
		count, float64(heap)/(1<<20), float64(usage.Maxrss)/1024, time.Since(start).Round(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if heap > 64<<20 {
		t.Fatalf("snapshot index exceeded heap limit: %d bytes", heap)
	}
}

func largeTreePath(n int) string {
	return fmt.Sprintf("/volume/tree/%05d/long-fixture-entry-with-repeated-path-%09d", n/100, n)
}
