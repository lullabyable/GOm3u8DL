package model

// TaskStatus represents the current state of a download task.
type TaskStatus int

const (
	TaskStatusPending    TaskStatus = iota
	TaskStatusParsing             // 解析中
	TaskStatusDownloading         // 下载中
	TaskStatusDecrypting          // 解密中
	TaskStatusMerging             // 合并中
	TaskStatusMuxing              // 封装中
	TaskStatusDone                // 完成
	TaskStatusFailed              // 失败
	TaskStatusCancelled           // 已取消
)

// String returns a human-readable status name.
func (s TaskStatus) String() string {
	switch s {
	case TaskStatusPending:
		return "pending"
	case TaskStatusParsing:
		return "parsing"
	case TaskStatusDownloading:
		return "downloading"
	case TaskStatusDecrypting:
		return "decrypting"
	case TaskStatusMerging:
		return "merging"
	case TaskStatusMuxing:
		return "muxing"
	case TaskStatusDone:
		return "done"
	case TaskStatusFailed:
		return "failed"
	case TaskStatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}
