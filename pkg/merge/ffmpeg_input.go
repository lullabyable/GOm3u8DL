package merge

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func prepareFFmpegInput(ctx context.Context, in FFmpegInput, work string, index int) ([]string, error) {
	for _, path := range append([]string{in.InitPath}, in.SegmentPaths...) {
		if path == "" {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", path, err)
		}
		if !fi.Mode().IsRegular() || fi.Size() == 0 {
			return nil, fmt.Errorf("empty or non-regular input: %s", path)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if in.InitPath != "" {
		// fMP4 fragments are NOT standalone concat-demuxer inputs: prepend init once,
		// then concatenate moof/mdat fragments into one seekable, continuous input.
		path := filepath.Join(work, fmt.Sprintf("input-%d.mp4", index))
		paths := append([]string{in.InitPath}, in.SegmentPaths...)
		if err := copyInputs(ctx, path, paths); err != nil {
			return nil, err
		}
		return []string{"-protocol_whitelist", "file", "-i", path}, nil
	}
	first, err := os.Open(in.SegmentPaths[0])
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 8)
	n, _ := io.ReadFull(first, hdr)
	first.Close()
	if n == 8 {
		switch string(hdr[4:8]) {
		case "moof", "styp", "sidx":
			return nil, fmt.Errorf("fragmented MP4 input requires its initialization segment")
		}
	}
	if len(in.SegmentPaths) == 1 {
		path, err := filepath.Abs(in.SegmentPaths[0])
		if err != nil {
			return nil, err
		}
		return []string{"-protocol_whitelist", "file", "-i", path}, nil
	}
	// Standalone TS segments: demuxer concatenation rebases per-segment timestamps
	// rather than naively joining reset timestamps. Paths are escaped, never shell input.
	var list strings.Builder
	list.WriteString("ffconcat version 1.0\n")
	for _, path := range in.SegmentPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(abs, "\r\n\x00") {
			return nil, fmt.Errorf("unsupported newline/NUL in input path")
		}
		escaped := strings.ReplaceAll(filepath.ToSlash(abs), "'", "'\\''")
		fmt.Fprintf(&list, "file '%s'\n", escaped)
	}
	path := filepath.Join(work, fmt.Sprintf("input-%d.ffconcat", index))
	if err := os.WriteFile(path, []byte(list.String()), 0600); err != nil {
		return nil, err
	}
	return []string{"-f", "concat", "-safe", "0", "-protocol_whitelist", "file", "-i", path}, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func copyInputs(ctx context.Context, output string, paths []string) (err error) {
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()
	buf := make([]byte, 256*1024)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(out, contextReader{ctx, f}, buf)
		closeErr := f.Close()
		if copyErr != nil {
			return fmt.Errorf("copy %s: %w", path, copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// Structural sanity check only, not a full decode or AV-sync validation.
func validateMP4(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	var ftyp, moov, mdat bool
	for offset := int64(0); offset < fi.Size(); {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(f, hdr); err != nil {
			return fmt.Errorf("incomplete MP4 box: %w", err)
		}
		size, headerSize := uint64(binary.BigEndian.Uint32(hdr)), uint64(8)
		if size == 1 {
			ext := make([]byte, 8)
			if _, err := io.ReadFull(f, ext); err != nil {
				return err
			}
			size, headerSize = binary.BigEndian.Uint64(ext), 16
		} else if size == 0 {
			size = uint64(fi.Size() - offset)
		}
		if size < headerSize || size > uint64(fi.Size()-offset) {
			return fmt.Errorf("invalid MP4 box length")
		}
		switch string(hdr[4:8]) {
		case "ftyp":
			ftyp = size > headerSize
		case "moov":
			moov = size > headerSize
		case "mdat":
			mdat = mdat || size > headerSize
		}
		offset += int64(size)
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}
	if !ftyp || !moov || !mdat {
		return fmt.Errorf("output missing nonempty ftyp/moov/mdat")
	}
	return nil
}
