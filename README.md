# GOm3u8DL

> 纯 Go 实现的流媒体下载器 SDK + CLI，重写自 [nilaoda/N_m3u8DL-RE](https://github.com/nilaoda/N_m3u8DL-RE)

跨平台流媒体下载工具，支持 **HLS (M3U8)** / **DASH (MPD)** / **MSS (ISM)** 三种协议，具备分段下载、解密、合并、字幕处理、直播录制等完整能力。

## 特性

- 🔑 **AES-128-CBC/ECB 自动解密** — 自动获取密钥并解密分段
- 📦 **纯 Go 合并** — 二进制拼接 / TS→MP4 remux / fMP4 合并，无需外部依赖
- ⚡ **并发下载** — 可配置分段并发数，支持速度限制
- 🎬 **多协议支持** — HLS / DASH / MSS 一站式解析
- 📝 **字幕处理** — WebVTT / TTML 解析与格式转换
- 🔄 **断点续传** — 下载状态持久化，支持中断恢复
- 📺 **直播录制** — HLS 直播流实时录制
- 🛠 **SDK 模式** — 可作为 Go 库嵌入 Wails / Electron / Flutter 等框架

## 安装

### 从源码编译

```bash
# 确保已安装 Go 1.21+
git clone git@github.com:lullabyable/GOm3u8DL.git
cd GOm3u8DL
go build -o m3u8dl ./cmd/m3u8dl/
```

### 使用 go install

```bash
go install github.com/lullabyable/GOm3u8DL/cmd/m3u8dl@latest
```

## 快速开始

### 基本下载

```bash
# 下载 HLS 流（自动选择最高画质）
./m3u8dl -url "https://example.com/video/index.m3u8" -auto-select

# 指定输出目录和文件名
./m3u8dl -url "https://example.com/video/index.m3u8" \
  -auto-select \
  -o ./output \
  -save-name my_video
```

### 带加密的 HLS

```bash
# 自动获取密钥并解密（默认行为）
./m3u8dl -url "https://example.com/encrypted/index.m3u8" -auto-select

# 手动指定解密密钥（kid:key 格式，十六进制）
./m3u8dl -url "https://example.com/encrypted/index.m3u8" \
  -key "00000000000000000000000000000001:0123456789abcdef0123456789abcdef"
```

### 自定义 HTTP 请求

```bash
# 添加自定义 Header（如 Referer、User-Agent）
./m3u8dl -url "https://example.com/video/index.m3u8" \
  -H "Referer: https://example.com/" \
  -H "User-Agent: Mozilla/5.0"
```

## 命令行参数

### 必选参数

| 参数 | 说明 |
|------|------|
| `-url` | M3U8 / MPD / ISM 播放列表 URL（必填） |

### 下载控制

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-auto-select` | `false` | 自动选择最高画质流（不弹出交互选择） |
| `-concurrency` | `8` | 分段下载并发数 |
| `-max-speed` | `0`（不限速） | 最大下载速度（bytes/sec），如 `1048576` = 1 MB/s |
| `-merge` | `binary` | 合并模式，可选：`binary` / `ts2mp4` / `fmp4` / `ffmpeg` |

### 输出控制

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-o` | `.`（当前目录） | 输出目录 |
| `-save-name` | `output` | 输出文件名（不含扩展名） |

### 解密相关

| 参数 | 说明 |
|------|------|
| `-key` | 手动指定解密密钥，格式 `kid:key`（十六进制），可重复指定多个 |

### HTTP 配置

| 参数 | 说明 |
|------|------|
| `-H` | 自定义 HTTP Header，格式 `Key: Value`，可重复使用 |

### 字幕相关

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-auto-subtitle-fix` | `false` | 自动修复字幕时间轴 |
| `-sub-only` | `false` | 仅下载字幕 |

### 其他

| 参数 | 说明 |
|------|------|
| `-version` | 显示版本信息 |

## 合并模式详解

| 模式 | 说明 | 依赖 | 输出格式 |
|------|------|------|---------|
| `binary` | 纯 Go 二进制拼接 TS 文件 | 无 | `.ts` |
| `ts2mp4` | 纯 Go TS→MP4 remux | 无 | `.mp4` |
| `fmp4` | 纯 Go fragmented MP4 合并 | 无 | `.mp4` |
| `ffmpeg` | 调用外部 ffmpeg 合并 | ffmpeg | `.mp4` |

```bash
# 使用 TS→MP4 remux（推荐，纯 Go 无需外部工具）
./m3u8dl -url "..." -auto-select -merge ts2mp4

# 使用 ffmpeg 合并（处理 Dolby Vision 等特殊格式）
./m3u8dl -url "..." -auto-select -merge ffmpeg -ffmpeg-path /usr/bin/ffmpeg
```

## 配置文件

GOm3u8DL 支持 JSON 配置文件，按以下顺序查找：

1. 环境变量 `M3U8DL_CONFIG` 指定的路径
2. `~/.config/m3u8dl/config.json`
3. 当前目录下的 `m3u8dl.json`

### 配置文件示例

```json
{
  "thread_count": 16,
  "max_speed": 0,
  "output_dir": "./downloads",
  "merge_mode": 0,
  "ffmpeg_path": "/usr/bin/ffmpeg",
  "del_after_done": true,
  "mux_after_done": false,
  "auto_subtitle_fix": false,
  "headers": {
    "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
    "Referer": "https://example.com/"
  },
  "proxy": "http://127.0.0.1:7890",
  "max_concurrent_tasks": 1,
  "retry_count": 3
}
```

### 配置项说明

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `thread_count` | int | `8` | 分段下载并发数 |
| `max_speed` | int64 | `0` | 最大下载速度（bytes/sec），0=不限 |
| `output_dir` | string | `.` | 默认输出目录 |
| `merge_mode` | int | `0` | 合并模式：0=binary, 1=ts2mp4, 2=fmp4, 3=ffmpeg |
| `ffmpeg_path` | string | `""` | ffmpeg 可执行文件路径 |
| `del_after_done` | bool | `false` | 下载完成后删除临时文件 |
| `mux_after_done` | bool | `false` | 下载完成后重新封装 |
| `auto_subtitle_fix` | bool | `false` | 自动修复字幕时间轴 |
| `headers` | object | `{}` | 默认 HTTP Headers |
| `proxy` | string | `""` | HTTP 代理地址 |
| `max_concurrent_tasks` | int | `1` | 同时下载的任务数 |
| `retry_count` | int | `3` | 分段下载失败重试次数 |

## 支持的协议

### HLS (M3U8)

- ✅ Master playlist 多码率解析
- ✅ Media playlist 分段解析
- ✅ AES-128-CBC / ECB 解密（自动获取密钥）
- ✅ #EXT-X-BYTERANGE 字节范围请求
- ✅ #EXT-X-MAP init segment
- ✅ 直播流录制（#EXT-X-ENDLIST 检测）

### DASH (MPD)

- ✅ SegmentTemplate 模式
- ✅ SegmentList 模式
- ✅ SegmentBase 模式
- ✅ 多 Period 处理
- ✅ BaseURL 嵌套拼接

### MSS (ISM)

- ✅ Microsoft Smooth Streaming 解析
- ✅ StreamIndex / QualityLevel / Chunk 解析

## SDK 使用（Go 库）

GOm3u8DL 可以作为 Go SDK 嵌入到任何 Go 项目中：

```go
package main

import (
    "context"
    "fmt"

    "github.com/lullabyable/GOm3u8DL/pkg/m3u8dl"
    "github.com/lullabyable/GOm3u8DL/pkg/model"
)

func main() {
    // 创建引擎
    engine := m3u8dl.New(
        m3u8dl.WithSegmentConcurrency(8),
    )

    ctx := context.Background()

    // 解析流
    streams, err := engine.GetStreams(ctx, "https://example.com/index.m3u8", nil)
    if err != nil {
        panic(err)
    }

    // 打印可用流
    for i, s := range streams {
        fmt.Printf("[%d] %s | %s | %d Kbps\n",
            i, s.Resolution, s.Codecs, s.Bandwidth/1000)
    }

    // 下载（带进度回调）
    handler := m3u8dl.EventHandlerFunc{
        OnProgressFn: func(e m3u8dl.ProgressEvent) {
            fmt.Printf("\r%.1f%% | %d KB/s | %d/%d segments",
                e.Percent, e.Speed/1024, e.SegmentsDone, e.Segments)
        },
        OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
            fmt.Printf("\nStatus: %s\n", e.Status)
        },
    }

    err = engine.Download(ctx, model.DownloadRequest{
        Stream:    &streams[0],
        OutputDir: "./output",
        SaveName:  "video",
        MergeMode: model.MergeModeBinary,
    }, handler)
}
```

### SDK 配置选项

```go
engine := m3u8dl.New(
    m3u8dl.WithSegmentConcurrency(16),      // 分段并发数
    m3u8dl.WithMaxConcurrentTasks(4),        // 同时下载任务数
    m3u8dl.WithGlobalMaxSpeed(10*1024*1024), // 全局限速 10MB/s
    m3u8dl.WithTempDir("/tmp/m3u8dl"),       // 临时目录
    m3u8dl.WithLogLevel(m3u8dl.LogDebug),    // 日志级别
)
```

## 常见用例

### 下载加密视频

```bash
# 大多数加密 HLS 会自动处理密钥获取和解密
./m3u8dl -url "https://example.com/encrypted/index.m3u8" -auto-select

# 如果需要指定 Referer 才能获取密钥
./m3u8dl -url "https://example.com/encrypted/index.m3u8" \
  -auto-select \
  -H "Referer: https://example.com/"
```

### 限速下载

```bash
# 限制下载速度为 2 MB/s
./m3u8dl -url "..." -auto-select -max-speed 2097152
```

### 下载后转为 MP4

```bash
# 使用纯 Go TS→MP4 remux
./m3u8dl -url "..." -auto-select -merge ts2mp4

# 使用 ffmpeg（处理特殊编码）
./m3u8dl -url "..." -auto-select -merge ffmpeg
```

### 仅下载字幕

```bash
./m3u8dl -url "..." -auto-select -sub-only -auto-subtitle-fix
```

### DASH (MPD) 下载

```bash
./m3u8dl -url "https://example.com/manifest.mpd" -auto-select
```

## 项目结构

```
GOm3u8DL/
├── cmd/m3u8dl/              # CLI 入口
│   └── main.go
├── pkg/                     # 公开 SDK
│   ├── m3u8dl/              # 引擎核心（Engine, Config, Events）
│   ├── model/               # 数据模型（Stream, Segment, Encrypt...）
│   ├── parser/              # 解析器
│   │   ├── hls/             # HLS M3U8 解析
│   │   ├── dash/            # DASH MPD 解析
│   │   └── mss/             # MSS ISM 解析
│   ├── downloader/          # 下载器（并发管理、进度追踪、限速）
│   ├── crypto/              # 解密（AES-128, ChaCha20）
│   ├── merge/               # 合并（binary, ts2mp4, fmp4, ffmpeg）
│   ├── mp4/                 # MP4 解析与 CENC 解密
│   └── subtitle/            # 字幕处理（WebVTT, TTML）
├── internal/                # 内部工具
│   ├── httputil/            # HTTP 工具
│   └── fileutil/            # 文件工具
├── go.mod
├── go.sum
└── README.md
```

## 依赖

| 库 | 用途 |
|----|------|
| `golang.org/x/crypto` | ChaCha20 解密 |
| Go 标准库 | HTTP / AES / XML / 文件 IO |

所有核心功能均为纯 Go 实现，无 CGO，支持交叉编译。

## 与原项目对比

| 维度 | N_m3u8DL-RE (C#) | GOm3u8DL (Go) |
|------|------------------|---------------|
| 运行时 | .NET Runtime | 单二进制，无依赖 |
| 外部工具 | ffmpeg + mp4decrypt + ffprobe | 仅 ffmpeg 可选 |
| TS→MP4 | 依赖 ffmpeg | 纯 Go (gomedia) |
| DRM 解密 | 依赖 mp4decrypt | 纯 Go (mp4ff-decrypt) |
| 进度获取 | 解析 stdout | 事件回调（结构化） |
| 并发模型 | 进程级 | goroutine 池（轻量） |
| 嵌入性 | 无法嵌入 | 直接 import |

## 许可证

MIT License
