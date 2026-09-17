package m3u8dl

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lullabyable/GOm3u8DL/pkg/downloader"
)

func cleanupMergedTemp(tempDir, output string) error {
	dir, err := filepath.Abs(tempDir)
	if err != nil {
		return err
	}
	final, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	// Resolve existing symlinks/junctions before deciding whether output is inside.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if resolved, err := filepath.EvalSymlinks(final); err == nil {
		final = resolved
	}
	rel, err := filepath.Rel(dir, final)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return fmt.Errorf("refusing to remove temp directory containing final output: %s", tempDir)
	}
	if dir == filepath.Dir(dir) {
		return fmt.Errorf("refusing to remove filesystem root")
	}
	return downloader.CleanupTemp(tempDir)
}
