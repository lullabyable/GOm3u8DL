package main

import (
	"fmt"
	"github.com/lullabyable/GOm3u8DL/pkg/m3u8dl"
)

func renderMergeProgress(e m3u8dl.ProgressEvent) {
	percent := "未知"
	if e.Percent >= 0 {
		percent = fmt.Sprintf("%.1f%%", e.Percent)
	}
	fmt.Printf("\r\033[K  合并 [%s] %s  媒体时间 %.1fs / %.1fs  %s", e.Phase, percent, e.MediaTime, e.MediaDuration, e.MergeSpeed)
	if e.Phase == "done" {
		fmt.Println()
	}
}
