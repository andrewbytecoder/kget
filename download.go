package pget

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

const (
	// Larger chunks reduce per-request KCP session overhead and smooth throughput.
	kcpChunkSizeDefault int64 = 4 << 20 // 4MiB
	kcpChunkMaxRetries       = 5
)

var kcpChunkSize = kcpChunkSizeDefault

func init() {
	// Optional override: PGET_KCP_CHUNK_SIZE=1048576
	if v := os.Getenv("PGET_KCP_CHUNK_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			kcpChunkSize = n
		}
	}
}

type assignTasksConfig struct {
	Procs         int
	TaskSize      int64 // download filesize per task
	ContentLength int64 // full download filesize
	PartialDir    string
	Filename      string
	KCPAddr       string
	RemotePath    string
}

type task struct {
	ID         int
	Procs      int
	Range      Range
	PartialDir string
	Filename   string
	KCPAddr    string
	RemotePath string
}

func (t *task) destPath() string {
	return getPartialFilePath(t.PartialDir, t.Filename, t.Procs, t.ID)
}

func (t *task) String() string {
	return fmt.Sprintf("task[%d]: %q", t.ID, t.destPath())
}

type makeRequestOption struct {
	useragent string
	referer   string
}

type progressReader struct {
	r   io.Reader
	bar *pb.ProgressBar
	n   int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n += int64(n)
		p.bar.Add64(int64(n))
	}
	return n, err
}

// assignTasks creates task to assign it to each goroutines
func assignTasks(c *assignTasksConfig) []*task {
	tasks := make([]*task, 0, c.Procs)

	for i := 0; i < c.Procs; i++ {

		r := makeRange(i, c.Procs, c.TaskSize, c.ContentLength)

		partName := getPartialFilePath(c.PartialDir, c.Filename, c.Procs, i)

		if info, err := os.Stat(partName); err == nil {
			infosize := info.Size()
			// check if the part is fully downloaded
			partTotal := (r.high - r.low) + 1
			// If previous buggy runs produced an oversized partial, truncate it back.
			if infosize > partTotal {
				if f, err := os.OpenFile(partName, os.O_WRONLY, 0666); err == nil {
					_ = f.Truncate(partTotal)
					_ = f.Close()
					logger.Warn("truncate oversized partial",
						slog.String("file", partName),
						slog.Int("part_id", i),
						slog.Int64("had", infosize),
						slog.Int64("want", partTotal),
					)
					infosize = partTotal
				}
			}
			if i == c.Procs-1 {
				if infosize >= partTotal {
					continue
				}
			} else if infosize >= partTotal {
				// skip as the part is already downloaded
				continue
			}

			// make low range from this next byte
			r.low += infosize
		}

		tasks = append(tasks, &task{
			ID:         i,
			Procs:      c.Procs,
			Range:      r,
			PartialDir: c.PartialDir,
			Filename:   c.Filename,
			KCPAddr:    c.KCPAddr,
			RemotePath: c.RemotePath,
		})
	}

	return tasks
}

type DownloadConfig struct {
	Filename      string
	Dirname       string
	ContentLength int64
	Procs         int
	KCPAddr       string
	RemotePath    string

	*makeRequestOption
}

type DownloadOption func(c *DownloadConfig)

func WithUserAgent(ua, version string) DownloadOption {
	return func(c *DownloadConfig) {
		if ua == "" {
			ua = "Pget/" + version
		}
		c.makeRequestOption.useragent = ua
	}
}

func WithReferer(referer string) DownloadOption {
	return func(c *DownloadConfig) {
		c.makeRequestOption.referer = referer
	}
}

func Download(ctx context.Context, c *DownloadConfig, opts ...DownloadOption) error {
	partialDir := getPartialDirname(c.Dirname, c.Filename, c.Procs)

	// create download location
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		return errors.Wrap(err, "failed to mkdir for download location")
	}

	c.makeRequestOption = &makeRequestOption{}

	for _, opt := range opts {
		opt(c)
	}

	tasks := assignTasks(&assignTasksConfig{
		Procs:         c.Procs,
		TaskSize:      c.ContentLength / int64(c.Procs),
		ContentLength: c.ContentLength,
		PartialDir:    partialDir,
		Filename:      c.Filename,
		KCPAddr:       c.KCPAddr,
		RemotePath:    c.RemotePath,
	})

	if err := parallelDownload(ctx, &parallelDownloadConfig{
		ContentLength:     c.ContentLength,
		Tasks:             tasks,
		PartialDir:        partialDir,
		makeRequestOption: c.makeRequestOption,
	}); err != nil {
		return err
	}

	return bindFiles(c, partialDir)
}

type parallelDownloadConfig struct {
	ContentLength int64
	Tasks         []*task
	PartialDir    string
	*makeRequestOption
}

func parallelDownload(ctx context.Context, c *parallelDownloadConfig) error {
	eg, ctx := errgroup.WithContext(ctx)

	bar := pb.Start64(c.ContentLength).SetWriter(stdout).Set(pb.Bytes, true)
	defer bar.Finish()

	// check file size already downloaded for resume
	size, err := checkProgress(c.PartialDir)
	if err != nil {
		return errors.Wrap(err, "failed to get directory size")
	}

	if size > c.ContentLength {
		// Don't allow progress bar to exceed total due to prior buggy partials.
		size = c.ContentLength
	}
	bar.SetCurrent(size)

	for _, task := range c.Tasks {
		task := task
		eg.Go(func() error {
			return task.download(ctx, c.makeRequestOption, bar)
		})
	}

	return eg.Wait()
}

func (t *task) download(ctx context.Context, opt *makeRequestOption, bar *pb.ProgressBar) error {
	output, err := os.OpenFile(t.destPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return errors.Wrapf(err, "failed to create: %q", t.String())
	}
	defer output.Close()

	if t.KCPAddr == "" {
		return errors.New("kcp addr is required")
	}

	remaining := (t.Range.high - t.Range.low) + 1
	if remaining <= 0 {
		return nil
	}

	offset := t.Range.low
	logger.Info("task start",
		slog.Int("task_id", t.ID),
		slog.Int64("offset", offset),
		slog.Int64("remaining", remaining),
		slog.String("file", t.RemotePath),
	)

	for remaining > 0 {
		chunk := remaining
		if chunk > kcpChunkSize {
			chunk = kcpChunkSize
		}

		logger.Debug("chunk begin",
			slog.Int("task_id", t.ID),
			slog.Int64("offset", offset),
			slog.Int64("len", chunk),
			slog.Int64("remaining", remaining),
		)

		var lastErr error
		for attempt := 1; attempt <= kcpChunkMaxRetries; attempt++ {
			// record file size for rollback on partial writes
			fi, statErr := output.Stat()
			if statErr != nil {
				return errors.Wrapf(statErr, "failed to stat output: %q", t.String())
			}
			startSize := fi.Size()

			body, err := kcpGetRange(ctx, t.KCPAddr, &kcpRangeRequest{
				Path:     t.RemotePath,
				Offset:   offset,
				Length:   chunk,
				ClientUA: opt.useragent,
				Referer:  opt.referer,
			})
			if err != nil {
				lastErr = err
				logger.Warn("kcp range dial/request failed",
					slog.Int("task_id", t.ID),
					slog.Int("attempt", attempt),
					slog.Int64("offset", offset),
					slog.Int64("len", chunk),
					slog.String("err", err.Error()),
				)
				time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
				continue
			}

			// Stream progress while reading, but rollback progress on failures/retries.
			pr := &progressReader{r: io.LimitReader(body, chunk), bar: bar}
			n, err := io.CopyN(output, pr, chunk)
			_ = body.Close()
			if err != nil {
				lastErr = err
				// rollback partial writes so retries do not duplicate bytes
				if terr := output.Truncate(startSize); terr != nil {
					return errors.Wrapf(terr, "failed to rollback partial write: %q", t.String())
				}
				if _, serr := output.Seek(0, io.SeekEnd); serr != nil {
					return errors.Wrapf(serr, "failed to seek after rollback: %q", t.String())
				}
				// rollback progress already added while streaming
				if pr.n > 0 {
					bar.Add64(-pr.n)
				}
				logger.Warn("rollback partial write",
					slog.Int("task_id", t.ID),
					slog.Int("attempt", attempt),
					slog.Int64("offset", offset),
					slog.Int64("intended_len", chunk),
					slog.Int64("written", n),
				)
				logger.Warn("kcp range copy failed",
					slog.Int("task_id", t.ID),
					slog.Int("attempt", attempt),
					slog.Int64("offset", offset),
					slog.Int64("len", chunk),
					slog.String("err", err.Error()),
				)
				time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
				continue
			}

			// Success.
			logger.Debug("chunk done",
				slog.Int("task_id", t.ID),
				slog.Int("attempt", attempt),
				slog.Int64("offset", offset),
				slog.Int64("len", chunk),
			)
			lastErr = nil
			break
		}

		if lastErr != nil {
			return errors.Wrapf(lastErr, "kcp range failed after retries: %q", t.String())
		}

		offset += chunk
		remaining -= chunk
	}

	logger.Info("task done", slog.Int("task_id", t.ID))

	return nil
}

func bindFiles(c *DownloadConfig, partialDir string) error {
	fmt.Fprintln(stdout, "\nbinding with files...")

	destPath := filepath.Join(c.Dirname, c.Filename)
	f, err := os.Create(destPath)
	if err != nil {
		return errors.Wrap(err, "failed to create a file in download location")
	}
	defer f.Close()

	bar := pb.Start64(c.ContentLength).SetWriter(stdout)

	for i := 0; i < c.Procs; i++ {
		partialFilename := getPartialFilePath(partialDir, c.Filename, c.Procs, i)
		// Copy only the expected bytes for this part, to avoid producing oversized outputs
		// if previous runs left extra bytes in the partial file.
		r := makeRange(i, c.Procs, c.ContentLength/int64(c.Procs), c.ContentLength)
		expected := (r.high - r.low) + 1
		subfp, err := os.Open(partialFilename)
		if err != nil {
			return errors.Wrapf(err, "failed to open %q in download location", partialFilename)
		}
		proxy := bar.NewProxyReader(subfp)
		_, err = io.CopyN(f, proxy, expected)
		_ = subfp.Close()
		if err != nil {
			return errors.Wrapf(err, "failed to copy %q", partialFilename)
		}

		// remove a file in download location for join
		if err := os.Remove(partialFilename); err != nil {
			return errors.Wrapf(err, "failed to remove %q in download location", partialFilename)
		}
	}

	bar.Finish()

	// remove download location
	// RemoveAll reason: will create .DS_Store in download location if execute on mac
	if err := os.RemoveAll(partialDir); err != nil {
		return errors.Wrap(err, "failed to remove download location")
	}

	return nil
}
