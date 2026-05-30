package merge

import (
	"fmt"
	"io"
	"os"
)

// BinaryMerge concatenates multiple files into a single output file.
// This is the simplest merge mode, suitable for TS segments.
func BinaryMerge(filePaths []string, outputPath string) error {
	if len(filePaths) == 0 {
		return fmt.Errorf("no files to merge")
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output %s: %w", outputPath, err)
	}
	defer out.Close()

	for i, path := range filePaths {
		if path == "" {
			return fmt.Errorf("empty path at index %d", i)
		}

		in, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}

		n, err := io.Copy(out, in)
		in.Close()
		if err != nil {
			return fmt.Errorf("copy %s (wrote %d bytes): %w", path, n, err)
		}
	}

	return nil
}

// BinaryMergeWithInit prepends an init segment before the media segments.
// Used for fMP4 streams where an init segment (ftyp+moov) is required.
func BinaryMergeWithInit(initPath string, segmentPaths []string, outputPath string) error {
	if initPath == "" {
		return BinaryMerge(segmentPaths, outputPath)
	}

	allPaths := make([]string, 0, 1+len(segmentPaths))
	allPaths = append(allPaths, initPath)
	allPaths = append(allPaths, segmentPaths...)
	return BinaryMerge(allPaths, outputPath)
}
