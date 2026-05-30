# N_m3u8DL-RE Go 重写评估报告

> 原项目: [nilaoda/N_m3u8DL-RE](https://github.com/nilaoda/N_m3u8DL-RE) (C# / .NET 10)
> 目标: Go 重写为**纯 Go SDK**，不绑定任何前端框架，可被 Wails / Electron / Flutter / Tauri / CLI 等任意消费者调用
> 尽可能用纯 Go 库替代外部工具依赖（ffmpeg / mp4decrypt），仅 Dolby Vision 等极端场景保留 ffmpeg 可选后备
> 评估日期: 2026-05-30

---

## 一、原项目概览

### 1.1 项目定位

跨平台流媒体下载器，支持 **HLS (M3U8)** / **DASH (MPD)** / **MSS (ISM)** 三种协议，具备分段下载、解密、合并、字幕处理、直播录制等完整能力。

### 1.2 代码规模

| 指标 | 数值 |
|------|------|
| C# 源文件 | **100 个** |
| 总代码量 | **~14,500 行** |
| 项目结构 | 3 个子项目 + 1 个测试项目 |
| 外部依赖 | 仅 2 个 NuGet 包 |

### 1.3 项目结构

```
N_m3u8DL-RE/
├── N_m3u8DL-RE.Common/          # 公共实体、枚举、工具
│   ├── Entity/                   # StreamSpec, MediaSegment, Playlist, EncryptInfo...
│   ├── Enum/                     # EncryptMethod, MediaType, ExtractorType...
│   ├── Util/                     # HTTPUtil, HexUtil, RetryUtil, BinaryContentCheckUtil
│   ├── Log/                      # Logger, CustomAnsiConsole
│   └── Resource/                 # 多语言字符串资源
│
├── N_m3u8DL-RE.Parser/          # 解析器层
│   ├── Extractor/
│   │   ├── IExtractor.cs         # 解析器接口
│   │   ├── HLSExtractor.cs       # HLS M3U8 解析 (573 行)
│   │   ├── DASHExtractor2.cs     # DASH MPD 解析 (639 行)
│   │   ├── MSSExtractor.cs       # MSS ISM 解析 (387 行)
│   │   └── LiveTSExtractor.cs    # 直播 TS 流解析
│   ├── Mp4/
│   │   ├── MP4Parser.cs          # MP4 Box 解析 (344 行)
│   │   ├── MP4InitUtil.cs        # Init Segment 解析 (91 行)
│   │   ├── MP4TtmlUtil.cs        # TTML 字幕提取
│   │   └── MP4VttUtil.cs         # WebVTT 字幕提取
│   ├── Processor/                # 内容/URL 处理器（可扩展）
│   └── Constants/                # HLS/DASH/MSS 标签常量
│
├── N_m3u8DL-RE/                 # 主程序
│   ├── Downloader/
│   │   ├── IDownloader.cs        # 下载器接口
│   │   └── SimpleDownloader.cs   # HTTP 分段下载 + 解密
│   ├── DownloadManager/
│   │   ├── SimpleDownloadManager.cs      # 点播下载管理 (777 行)
│   │   ├── SimpleLiveRecordManager2.cs   # 直播录制管理 (933 行)
│   │   └── HTTPLiveRecordManager.cs      # HTTP 直播录制 (254 行)
│   ├── Crypto/
│   │   ├── AESUtil.cs            # AES-128-CBC/ECB 解密
│   │   └── ChaCha20Util.cs       # ChaCha20 解密 (37 行)
│   ├── Util/
│   │   ├── DownloadUtil.cs       # HTTP 下载实现
│   │   ├── MergeUtil.cs          # 文件合并 (binary / ffmpeg) (285 行)
│   │   ├── MP4DecryptUtil.cs     # mp4decrypt/shaka-packager 调用
│   │   ├── SubtitleUtil.cs       # 字幕处理
│   │   ├── LargeSingleFileSplitUtil.cs  # 大文件切片并行下载
│   │   └── FilterUtil.cs         # 流过滤
│   ├── CommandLine/
│   │   └── CommandInvoker.cs     # CLI 参数解析 (776 行)
│   └── Config/
│       └── DownloaderConfig.cs   # 下载配置
│
└── N_m3u8DL-RE.Tests/           # 单元测试
```

### 1.4 外部依赖

#### 原项目 C# 依赖

| NuGet 包 | 用途 | Go 替代方案 |
|-----------|------|------------|
| `System.CommandLine` | CLI 参数解析 | `cobra` / `urfave/cli`（CLI 消费者用） |
| `Spectre.Console` | 终端进度条/表格 UI | 不需要（SDK 层不做 UI） |
| `System.Security.Cryptography` | AES/ChaCha20 | Go `crypto/aes` 标准库 |
| `System.Xml.Linq` | MPD XML 解析 | Go `encoding/xml` 标准库 |
| `System.Net.Http` | HTTP 客户端 | Go `net/http` 标准库 |

#### Go 第三方依赖

| Go 库 | Stars | 用途 | 必要性 |
|-------|-------|------|--------|
| `github.com/yapingcat/gomedia` | 501 | TS 解复用 + MP4 封装（纯 Go，无 CGO） | P1：TS→MP4 remux |
| `github.com/Eyevinn/mp4ff` | 636 | fragmented MP4 读写 + CENC 解密（纯 Go） | P1：fMP4 处理 |
| `github.com/abema/go-mp4` | 545 | MP4 Box 读写（纯 Go） | P1：MP4 解析 |
| `golang.org/x/crypto` | — | ChaCha20 解密 | P0 |

**依赖评估：**
- **Phase 1 (MVP)：零外部依赖**，仅用 Go 标准库完成 HLS + AES + binary merge
- **Phase 2：引入 3 个纯 Go 库**，实现 TS→MP4 remux + fMP4 + MP4 解析
- **所有依赖均为纯 Go 实现，无 CGO，交叉编译无障碍**

---

## 二、核心架构设计

### 2.1 设计原则

**与前端完全解耦。** Go 项目只提供：

1. **Go SDK（库）** — 任何 Go 程序 `import` 即用
2. **事件驱动回调** — 消费者注册回调函数获取进度/状态/日志
3. **可选 CLI** — 独立的命令行入口，调用 SDK

```
┌─────────────────────────────────────────────────┐
│                  消费者层 (Consumer)              │
│  ┌──────────┐ ┌──────────┐ ┌──────┐ ┌────────┐ │
│  │  Wails   │ │ Electron │ │ CLI  │ │ Flutter│ │
│  │  Frontend│ │ Frontend │ │      │ │ via FFI│ │
│  └────┬─────┘ └────┬─────┘ └──┬───┘ └───┬────┘ │
│       │             │          │          │      │
│  ┌────▼─────────────▼──────────▼──────────▼────┐ │
│  │              Go SDK (golib)                 │ │
│  │  parser / downloader / crypto / merge / mp4 │ │
│  └─────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────┘
```

### 2.2 推荐目录结构

```
github.com/yourname/m3u8dl/
├── go.mod
│
├── pkg/                          # 公开 SDK（消费者 import 这里）
│   ├── m3u8dl/                   # 顶层包，统一入口
│   │   ├── engine.go             # Engine — 下载引擎主入口
│   │   ├── options.go            # 全局配置项
│   │   └── events.go             # 事件类型定义
│   │
│   ├── model/                    # 数据模型（纯数据，无逻辑）
│   │   ├── stream.go             # StreamInfo
│   │   ├── segment.go            # MediaSegment
│   │   ├── playlist.go           # Playlist
│   │   ├── encrypt.go            # EncryptInfo / EncryptMethod
│   │   ├── task.go               # DownloadTask / TaskStatus
│   │   └── result.go             # DownloadResult / MergeResult
│   │
│   ├── parser/                   # 解析器
│   │   ├── parser.go             # Parser 接口
│   │   ├── hls/                  # HLS M3U8
│   │   │   ├── extractor.go
│   │   │   ├── tags.go
│   │   │   └── key_processor.go
│   │   ├── dash/                 # DASH MPD
│   │   │   ├── extractor.go
│   │   │   └── template.go
│   │   └── mss/                  # MSS ISM
│   │       └── extractor.go
│   │
│   ├── downloader/               # 下载器
│   │   ├── segment.go            # 单分段下载
│   │   ├── manager.go            # 下载编排管理器
│   │   └── progress.go           # 进度追踪器
│   │
│   ├── crypto/                   # 解密
│   │   ├── aes.go
│   │   └── chacha20.go
│   │
│   ├── merge/                    # 合并（纯 Go 为主，ffmpeg 为可选后备）
│   │   ├── binary.go             # 纯 Go 二进制拼接 TS 文件
│   │   ├── ts2mp4.go             # 用 gomedia 做 TS → MP4 remux（纯 Go）
│   │   ├── fmp4.go               # 用 mp4ff 处理 fragmented MP4（纯 Go）
│   │   └── ffmpeg.go             # 可选：调外部 ffmpeg（Dolby Vision 等极端场景）
│   │
│   ├── mp4/                      # MP4 解析（基于 abema/go-mp4）
│   │   ├── parser.go             # MP4 Box 解析
│   │   ├── init.go               # Init Segment 解析
│   │   ├── decrypt.go            # CENC 解密（基于 mp4ff-decrypt）
│   │   └── subtitle.go           # 字幕轨提取
│   │
│   └── subtitle/                 # 字幕处理
│       ├── webvtt.go
│       └── ttml.go
│
├── cmd/                          # 可选：CLI 入口
│   └── m3u8dl/
│       └── main.go               # 调用 pkg/m3u8dl，提供命令行界面
│
└── internal/                     # 内部实现（不导出）
    ├── httputil/                 # HTTP 工具（重试、限速、Header 处理）
    └── fileutil/                 # 文件工具（临时目录、清理）
```

---

## 三、模块逐一分析

### 3.1 解析器层 (parser)

#### 3.1.1 HLS Extractor (`HLSExtractor.cs` — 573 行)

**职责：** 解析 M3U8 master playlist 和 media playlist，提取音视频流信息、分段列表、加密信息。

**核心逻辑：**
- 解析 `#EXT-X-STREAM-INF` 提取多码率流
- 解析 `#EXT-X-MEDIA` 提取音频/字幕轨道
- 解析 `#EXT-X-MAP` 提取 init segment
- 解析 `#EXT-X-KEY` 提取加密信息（METHOD, KEYFORMAT, IV）
- 解析 `#EXT-X-BYTERANGE` 提取字节范围请求
- 处理 `#EXT-X-DISCONTINUITY` 分段不连续
- 支持 master playlist 嵌套引用

**Go 重写难度：⭐⭐⭐（中等）**

纯文本解析，Go 的 `bufio.Scanner` + 字符串处理完全胜任。需要注意：
- 属性解析（`KEYFORMAT="identity"` 这类带引号的 KV）
- URL 拼接逻辑（相对路径 → 绝对路径）
- 多层 playlist 递归解析

#### 3.1.2 DASH Extractor (`DASHExtractor2.cs` — 639 行)

**职责：** 解析 MPD (Media Presentation Description) 文件，提取 AdaptationSet、Representation、SegmentTemplate/SegmentList。

**核心逻辑：**
- XML 解析 Period → AdaptationSet → Representation 层级
- SegmentTemplate 模式（`$Number$`, `$Time$`, `$Bandwidth$` 变量替换）
- SegmentList 模式（显式分段列表）
- SegmentBase 模式（单文件字节范围）
- 多 Period 处理
- BaseURL 嵌套拼接
- `@timescale` / `@duration` 时钟计算

**Go 重写难度：⭐⭐⭐⭐（较高）**

DASH 规范比 HLS 复杂得多，XML 命名空间处理、时钟精度计算、模板变量替换都需要细致实现。Go 的 `encoding/xml` 可以处理，但需要手动管理命名空间。

#### 3.1.3 MSS Extractor (`MSSExtractor.cs` — 387 行)

**职责：** 解析 Microsoft Smooth Streaming (ISM) 清单。

**Go 重写难度：⭐⭐⭐（中等）**

类似 DASH，XML 解析 + 二进制 header 构造（mssMoov）。

#### 3.1.4 MP4 工具 (mp4/ — 435+ 行)

**职责：**
- `MP4Parser.cs` (344 行)：解析 MP4 Box 结构（moov/trak/moof 等）
- `MP4InitUtil.cs` (91 行)：从 init segment 提取 KID (Track ID)
- `MP4TtmlUtil.cs` / `MP4VttUtil.cs`：从 MP4 提取字幕轨

**Go 重写难度：⭐⭐（较低，有现成库）**

Go 生态有三个成熟纯 Go 库可直接使用：
- `abema/go-mp4` (545 stars) — MP4 Box 读写，替代手写 Box 解析
- `Eyevinn/mp4ff` (636 stars) — fragmented MP4 读写 + CENC 解密
- `yapingcat/gomedia` (501 stars) — TS/MP4 解复用 + 封装

不需要手写 Box 解析，直接调用库 API。

### 3.2 下载器层 (downloader)

#### 3.2.1 单分段下载器 (`SimpleDownloader.cs`)

**职责：** 单分段 HTTP 下载，支持：
- Range 请求（字节范围）
- 自动重试（可配置次数）
- 速度监控
- 下载后自动解密（AES-128-CBC/ECB, ChaCha20）
- Gzip 解压
- Image header 检测
- 断点跳过（已存在文件自动跳过）

**Go 重写难度：⭐⭐（较低）**

Go 的 `net/http` + `io.Copy` + goroutine 天然适合。核心代码量约 200 行。

#### 3.2.2 下载管理器 (`SimpleDownloadManager.cs` — 777 行)

**职责：** 编排整个下载流程：
1. 解析流信息 → 用户选择流
2. 创建临时目录
3. 并发下载所有分段（`ConcurrentDictionary`）
4. 进度追踪（Spectre.Console ProgressTask）
5. 大文件自动切片并行下载
6. 下载后解密
7. 文件合并（binary concat / ffmpeg）
8. MP4 DRM 解密（调用 mp4decrypt/shaka-packager）
9. 字幕后处理（WebVTT/TTML 时间轴修复）
10. Mux 封装（调用 ffmpeg）
11. 清理临时文件

**Go 重写难度：⭐⭐⭐⭐（最高）**

这是整个项目的核心编排层。Go 的优势：
- `sync.WaitGroup` + goroutine 池 替代 `ConcurrentDictionary`
- channel 替代进度回调
- `context.Context` 替代 `CancellationToken`

#### 3.2.3 直播录制管理器 (`SimpleLiveRecordManager2.cs` — 933 行)

**职责：** HLS/DASH 直播流录制，定时刷新 playlist、下载新分段、实时合并。

**Go 重写难度：⭐⭐⭐⭐（较高）**

需要 ticker 定时刷新、实时写入、直播结束检测。

### 3.3 加密层 (crypto)

| 文件 | 算法 | 行数 | Go 标准库替代 |
|------|------|------|--------------|
| `AESUtil.cs` | AES-128-CBC/ECB | ~40 | `crypto/aes` + `crypto/cipher` |
| `ChaCha20Util.cs` | ChaCha20 (每 1024 字节加密) | 37 | `golang.org/x/crypto/chacha20` |

**Go 重写难度：⭐（低）**

Go 标准库原生支持，`crypto/aes` + `crypto/cipher.NewCBCDecrypter` 几乎 1:1 映射。

### 3.4 合并层 (merge)

**职责：**
- Binary concat：多个 TS 文件二进制拼接
- TS → MP4 remux：用 `gomedia` 纯 Go 实现 TS 解复用 + MP4 封装
- fMP4 处理：用 `mp4ff` 处理 HLS 的 fragmented MP4 分段
- FFmpeg concat（可选后备）：调用 ffmpeg 处理 Dolby Vision 等特殊格式
- Partial combine：超过 90000 文件时分批预合并

**Go 重写方案（三级合并策略）：**

| 级别 | 方案 | 文件 | 依赖 | 适用场景 |
|------|------|------|------|---------|
| L1 | Binary concat | `binary.go` | 无 | 普通 TS 流，90% 场景 |
| L2 | TS→MP4 remux | `ts2mp4.go` | `gomedia` | 需要输出 MP4 |
| L3 | fMP4 处理 | `fmp4.go` | `mp4ff` | HLS fMP4 分段 |
| L4 | FFmpeg 后备 | `ffmpeg.go` | 外部 ffmpeg | Dolby Vision、特殊封装 |

**Go 重写难度：⭐⭐（较低）**

- L1 纯 Go `io.Copy` 拼接，最简单
- L2 使用 `gomedia` 的 `go-mpeg2/ts-demuxer` + `go-mp4/mp4muxer`，API 清晰
- L3 使用 `mp4ff` 的 fragment 读写能力
- L4 仅作后备，`os/exec` 调外部 ffmpeg

**关键依赖能力：**

```
gomedia (纯 Go, 501 stars)
├── go-mpeg2/ts-demuxer.go   → TS 分段解复用（提取 H264/H265/AAC 裸流）
├── go-mpeg2/ts-muxer.go     → TS 封装
├── go-mp4/mp4muxer.go       → MP4 封装器（将裸流写入 MP4）
└── go-mp4/mp4demuxer.go     → MP4 解复用

mp4ff (纯 Go, 636 stars)
├── mp4/fragment.go           → fragmented MP4 读写
├── cmd/mp4ff-decrypt/        → CENC/CTR 模式 MP4 解密
└── cmd/mp4ff-info/           → MP4 结构分析
```

### 3.5 其他工具

| 文件 | 功能 | Go 重写难度 |
|------|------|------------|
| `DownloadUtil.cs` | HTTP 下载底层实现 | ⭐ |
| `FilterUtil.cs` | 流过滤（按语言/带宽等） | ⭐ |
| `SubtitleUtil.cs` | 字幕处理 | ⭐⭐ |
| `LargeSingleFileSplitUtil.cs` | 大文件切片并行 | ⭐⭐ |
| `MP4DecryptUtil.cs` | 调用外部 DRM 工具 | ⭐⭐ |
| `ImageHeaderUtil.cs` | TS 文件头检测 | ⭐ |
| `LanguageCodeUtil.cs` | 语言代码标准化 | ⭐ |
| `MediainfoUtil.cs` | ffprobe 媒体信息检测 | ⭐ |

---

## 四、SDK 公开接口设计

### 4.1 核心引擎

```go
package m3u8dl

import (
    "context"
    "github.com/yourname/m3u8dl/pkg/model"
)

// Engine 是整个下载引擎的入口
type Engine struct {
    opts Options
}

// New 创建下载引擎实例
func New(opts ...Option) *Engine

// GetStreams 解析 URL，返回可用流列表
// 消费者拿到流列表后自行选择（或自动选择），再调用 Download
func (e *Engine) GetStreams(ctx context.Context, url string, headers map[string]string) ([]model.StreamInfo, error)

// Download 下载指定流
// 通过 onEvent 回调所有事件（进度、状态、日志、错误）
func (e *Engine) Download(ctx context.Context, req model.DownloadRequest, onEvent EventHandler) error

// DownloadWithAutoSelect 自动选择最佳流并下载
func (e *Engine) DownloadWithAutoSelect(ctx context.Context, url string, onEvent EventHandler) error
```

### 4.2 数据模型

```go
package model

// ---- 流信息 ----

type MediaType string
const (
    MediaTypeVideo     MediaType = "video"
    MediaTypeAudio     MediaType = "audio"
    MediaTypeSubtitles MediaType = "subtitles"
)

type StreamInfo struct {
    MediaType    MediaType
    GroupID      string
    Language     string
    Name         string
    Bandwidth    int
    Codecs       string
    Resolution   string   // "1920x1080"
    FrameRate    float64
    Channels     string
    Extension    string
    VideoRange   string   // "SDR" / "PQ" / "HLG"
    URL          string
    Playlist     *Playlist
    SegmentsCount int
}

// ---- 播放列表 ----

type Playlist struct {
    URL             string
    IsLive          bool
    RefreshInterval float64          // ms
    TotalDuration   float64          // seconds
    TargetDuration  *float64
    MediaInit       *MediaSegment
    MediaParts      []MediaPart
}

type MediaPart struct {
    MediaSegments []MediaSegment
}

type MediaSegment struct {
    Index        int64
    Duration     float64
    URL          string
    StartRange   *int64
    ExpectLength *int64
    EncryptInfo  EncryptInfo
}

// ---- 加密 ----

type EncryptMethod int
const (
    EncryptMethodNone EncryptMethod = iota
    EncryptMethodAES128
    EncryptMethodAES128ECB
    EncryptMethodSampleAES
    EncryptMethodSampleAESCTR
    EncryptMethodCENC
    EncryptMethodChaCha20
)

type EncryptInfo struct {
    Method EncryptMethod
    Key    []byte
    IV     []byte
}

// ---- 下载请求 ----

type MergeMode int
const (
    MergeModeBinary  MergeMode = iota // 纯 Go 二进制拼接 TS（默认，最快）
    MergeModeTS2MP4                   // 纯 Go TS→MP4 remux（gomedia）
    MergeModeFMP4                     // 纯 Go fragmented MP4（mp4ff）
    MergeModeFFmpeg                   // 调外部 ffmpeg（Dolby Vision 等）
)

type DownloadRequest struct {
    // 直接指定流（跳过 GetStreams）
    Stream *StreamInfo
    // 或者指定 URL，由引擎解析
    URL    string
    // 自动选择条件
    AutoSelect *AutoSelectRule
    // 输出配置
    OutputDir  string
    SaveName   string
    // HTTP 配置
    Headers    map[string]string
    // 下载配置
    ThreadCount       int    // 分段并发数
    MaxSpeed          int64  // bytes/sec, 0=不限
    DownloadRetryCount int
    // 合并配置
    MergeMode         MergeMode // 合并策略（见下文）
    FFmpegPath        string    // ffmpeg 路径（仅 MergeModeFFmpeg 时需要）
    MuxAfterDone      bool      // 完成后 mux 为 mp4
    DelAfterDone      bool      // 完成后删除临时文件
    // 解密配置
    Keys              []string // kid:key 对
    KeyFile           string   // key 文件路径
    // DRM 解密（仅用于需要外部工具的场景）
    DecryptionBin     string   // mp4decrypt/shaka-packager 路径（可选，纯 Go 解密优先）
    // 字幕配置
    AutoSubtitleFix   bool
    SubOnly           bool
}

type AutoSelectRule struct {
    MaxResolution  string // "1080p"
    PreferredLang  string // "zh"
    PreferredCodec string // "avc1"
}

// ---- 任务状态 ----

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

// ---- 下载结果 ----

type DownloadResult struct {
    TaskID     string
    Status     TaskStatus
    OutputPath string
    Duration   float64     // 总耗时(秒)
    FileSize   int64
    Error      error
}
```

### 4.3 事件系统（解耦核心）

```go
package m3u8dl

import "github.com/yourname/m3u8dl/pkg/model"

// EventHandler 消费者实现此接口接收所有事件
// 这是 SDK 与前端解耦的关键 — 不依赖任何 UI 框架
type EventHandler interface {
    // OnProgress 下载进度更新（高频调用，约每秒多次）
    OnProgress(event ProgressEvent)
    // OnStatusChange 任务状态变更
    OnStatusChange(event StatusEvent)
    // OnLog 日志输出
    OnLog(event LogEvent)
    // OnStreamInfo 解析完成，返回流信息
    OnStreamInfo(streams []model.StreamInfo)
}

// 也支持函数式注册（消费者不用实现完整接口）
type EventHandlerFunc struct {
    OnProgressFn    func(ProgressEvent)
    OnStatusChangeFn func(StatusEvent)
    OnLogFn         func(LogEvent)
    OnStreamInfoFn  func([]model.StreamInfo)
}

func (f EventHandlerFunc) OnProgress(e ProgressEvent) {
    if f.OnProgressFn != nil { f.OnProgressFn(e) }
}
func (f EventHandlerFunc) OnStatusChange(e StatusEvent) {
    if f.OnStatusChangeFn != nil { f.OnStatusChangeFn(e) }
}
func (f EventHandlerFunc) OnLog(e LogEvent) {
    if f.OnLogFn != nil { f.OnLogFn(e) }
}
func (f EventHandlerFunc) OnStreamInfo(s []model.StreamInfo) {
    if f.OnStreamInfoFn != nil { f.OnStreamInfoFn(s) }
}

// ---- 事件结构 ----

type ProgressEvent struct {
    TaskID       string
    Total        int64   // 总字节数
    Downloaded   int64   // 已下载字节数
    Speed        int64   // 当前速度 bytes/sec
    AvgSpeed     int64   // 平均速度
    Segments     int     // 总分段数
    SegmentsDone int     // 已完成分段数
    Percent      float64 // 0.0 ~ 100.0
    ETA          float64 // 预估剩余秒数
}

type StatusEvent struct {
    TaskID string
    Status model.TaskStatus
    Error  error
}

type LogLevel int
const (
    LogDebug LogLevel = iota
    LogInfo
    LogWarn
    LogError
)

type LogEvent struct {
    Level   LogLevel
    Message string
}
```

### 4.4 配置选项

```go
package m3u8dl

type Options struct {
    // 全局并发任务数（同时下载几个视频）
    MaxConcurrentTasks int
    // 单任务内分段并发数
    SegmentConcurrency int
    // 全局速度限制 bytes/sec
    GlobalMaxSpeed     int64
    // 临时文件目录
    TempDir            string
    // ffprobe 路径（媒体信息检测）
    FFProbePath        string
    // 日志级别
    LogLevel           LogLevel
}

type Option func(*Options)

func WithMaxConcurrentTasks(n int) Option
func WithSegmentConcurrency(n int) Option
func WithGlobalMaxSpeed(bytesPerSec int64) Option
func WithTempDir(dir string) Option
func WithFFProbePath(path string) Option
func WithLogLevel(level LogLevel) Option
```

---

## 五、消费者接入示例

### 5.1 CLI 消费者

```go
package main

import (
    "context"
    "fmt"
    "github.com/yourname/m3u8dl/pkg/m3u8dl"
    "github.com/yourname/m3u8dl/pkg/model"
)

func main() {
    engine := m3u8dl.New(
        m3u8dl.WithSegmentConcurrency(8),
    )

    // 解析流
    streams, err := engine.GetStreams(context.Background(), "https://...", nil)
    if err != nil {
        panic(err)
    }

    // 打印流列表供用户选择
    for i, s := range streams {
        fmt.Printf("[%d] %s | %s | %d Kbps\n", i, s.Resolution, s.Codecs, s.Bandwidth/1000)
    }

    // 下载
    handler := m3u8dl.EventHandlerFunc{
        OnProgressFn: func(e m3u8dl.ProgressEvent) {
            fmt.Printf("\r%.1f%% | %d KB/s | %d/%d segments", 
                e.Percent, e.Speed/1024, e.SegmentsDone, e.Segments)
        },
        OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
            fmt.Printf("\nStatus: %d\n", e.Status)
        },
    }

    err = engine.Download(context.Background(), model.DownloadRequest{
        Stream:    &streams[0],
        OutputDir: "./output",
        SaveName:  "video",
    }, handler)
}
```

### 5.2 Wails 消费者

```go
package app

import (
    "context"
    "github.com/yourname/m3u8dl/pkg/m3u8dl"
    "github.com/yourname/m3u8dl/pkg/model"
    "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
    engine *m3u8dl.Engine
    ctx    context.Context
}

func (a *App) Download(url string) (string, error) {
    handler := m3u8dl.EventHandlerFunc{
        OnProgressFn: func(e m3u8dl.ProgressEvent) {
            // 通过 Wails 事件推送到前端
            runtime.EventsEmit(a.ctx, "download:progress", e)
        },
        OnStatusChangeFn: func(e m3u8dl.StatusEvent) {
            runtime.EventsEmit(a.ctx, "download:status", e)
        },
    }

    err := a.engine.DownloadWithAutoSelect(a.ctx, url, handler)
    return "done", err
}

func (a *App) GetStreams(url string) ([]model.StreamInfo, error) {
    return a.engine.GetStreams(a.ctx, url, nil)
}
```

### 5.3 Electron 消费者（通过 gRPC / JSON-RPC）

```go
// 将 SDK 包装为 gRPC 服务
service M3U8Downloader {
    rpc GetStreams(StreamRequest) returns (StreamList);
    rpc Download(DownloadRequest) returns (stream ProgressEvent);
}
```

### 5.4 Flutter 消费者（通过 FFI）

```go
//export Download
func Download(url *C.char, callback C.ProgressCallback) {
    engine := m3u8dl.New()
    handler := m3u8dl.EventHandlerFunc{
        OnProgressFn: func(e m3u8dl.ProgressEvent) {
            // 通过 C 回调传给 Flutter
            C.callProgress(callback, C.double(e.Percent))
        },
    }
    engine.DownloadWithAutoSelect(context.Background(), C.GoStr(url), handler)
}
```

---

## 六、工作量估算

### 6.1 按模块估算

| 模块 | 原 C# 行数 | 预估 Go 行数 | 工时估算 | 优先级 |
|------|-----------|-------------|---------|--------|
| **model/** (数据模型) | ~400 | ~300 | 1 天 | P0 |
| **m3u8dl/** (Engine + Events + Options) | — | ~400 | 2 天 | P0 |
| **parser/hls/** (HLS 解析) | ~700 | ~600 | 3 天 | P0 |
| **crypto/** (AES + ChaCha20) | ~80 | ~60 | 0.5 天 | P0 |
| **downloader/segment.go** (HTTP 下载) | ~300 | ~250 | 2 天 | P0 |
| **downloader/manager.go** (编排) | ~777 | ~600 | 4 天 | P0 |
| **merge/** (文件合并) | ~285 | ~300 | 2 天 | P0 |
| **parser/dash/** (DASH 解析) | ~640 | ~550 | 3 天 | P1 |
| **mp4/** (MP4 解析) | ~435 | ~350 | 2 天 | P1 |
| **subtitle/** (字幕处理) | ~200 | ~150 | 1 天 | P1 |
| **downloader/live** (直播录制) | ~1200 | ~900 | 5 天 | P2 |
| **parser/mss/** (MSS 解析) | ~387 | ~300 | 2 天 | P2 |
| **cmd/m3u8dl/** (CLI 入口) | ~776 | ~300 | 2 天 | P1 |
| **单元测试** | ~200 | ~500 | 3 天 | P1 |
| **合计** | ~14,500 | **~5,560** | **~32 天** | — |

### 6.2 分阶段交付

```
Phase 1 — 核心 SDK (MVP)                    预计 10-12 天
├── model 层（全部数据结构）
├── Engine 框架 + 事件系统 + 配置
├── HLS 解析器
├── HTTP 并发分段下载 + 速度限制 + 重试
├── AES-128-CBC/ECB 解密
├── TS binary 合并（纯 Go，零依赖）
├── CLI 入口（基础）
└── 单元测试（model + parser + crypto）

Phase 2 — 功能完善                           预计 10-12 天
├── DASH (MPD) 解析器
├── MP4 解析（abema/go-mp4）
├── TS→MP4 remux（gomedia，纯 Go）
├── fMP4 处理（mp4ff，纯 Go）
├── CENC 解密（mp4ff-decrypt，纯 Go）
├── 字幕处理（WebVTT / TTML）
├── 多任务并发管理
├── 断点续传
├── CLI 完善（流选择、过滤、所有参数）
└── 集成测试

Phase 3 — 高级特性                           预计 8-10 天
├── 直播录制
├── MSS (ISM) 解析器
├── 大文件切片并行
├── 全局限速
├── ffmpeg 后备合并（可选，Dolby Vision 等）
├── 配置文件读取
└── 文档 + 示例
```

---

## 七、Go 标准库与第三方库对应关系

### 7.1 标准库

| C# 依赖 | Go 标准库/替代 |
|---------|---------------|
| `System.Net.Http.HttpClient` | `net/http.Client` |
| `System.Security.Cryptography.Aes` | `crypto/aes` + `crypto/cipher` |
| `System.Xml.Linq` | `encoding/xml` |
| `System.IO.FileStream` | `os.File` + `io.Copy` |
| `System.CommandLine` | `cobra` / `urfave/cli`（CLI 消费者自选） |
| `Spectre.Console` | 不需要（SDK 层不做 UI） |
| `ConcurrentDictionary` | `sync.Map` 或 `sync.RWMutex` + `map` |
| `CancellationToken` | `context.Context` |
| `Task.WhenAll` | `sync.WaitGroup` + goroutine |
| `SemaphoreSlim` | `chan struct{}` (带缓冲 channel) |
| `HttpClient.SendAsync` | `http.Client.Do` + `io.Copy` |

### 7.2 第三方库（替代原项目外部工具依赖）

| 原项目外部依赖 | 原项目调用方式 | Go 替代 | 类型 |
|---------------|--------------|---------|------|
| ffmpeg（合并/封装） | `Process.Start("ffmpeg", ...)` | `gomedia` TS→MP4 remux | 纯 Go |
| ffmpeg（Dolby Vision mux） | `Process.Start("ffmpeg", ...)` | `os/exec` 调 ffmpeg（可选后备） | 外部工具 |
| mp4decrypt（DRM 解密） | `Process.Start("mp4decrypt", ...)` | `mp4ff-decrypt` | 纯 Go |
| shaka-packager（DRM 解密） | `Process.Start("packager", ...)` | `mp4ff-decrypt` | 纯 Go |
| ffprobe（媒体信息检测） | `Process.Start("ffprobe", ...)` | `gomedia` 解析 或 `os/exec` 调 ffprobe | 混合 |

**关键变化：原项目依赖 3 个外部工具（ffmpeg / mp4decrypt / ffprobe），Go 版本通过纯 Go 库将其中 2 个完全内化，仅 ffmpeg 作为可选后备保留。**

---

## 八、风险与难点

### 8.1 技术风险

| 风险项 | 风险等级 | 说明 | 缓解方案 |
|--------|---------|------|---------|
| DASH 模板变量替换 | 🟡 中 | `$Number$`, `$Time$` 等模板变量的边界情况 | 参考原项目单测 + DASH-IF 规范 |
| MP4 Box 解析 | 🟢 低 | 有成熟纯 Go 库 | 使用 `abema/go-mp4` + `Eyevinn/mp4ff` |
| 加密兼容性 | 🟡 中 | SAMPLE-AES / CENC 等复杂加密模式 | Phase 1 只做 AES-128，CENC 用 mp4ff-decrypt |
| 直播录制稳定性 | 🔴 高 | 网络抖动、playlist 刷新时序 | 参考原项目 933 行实现，充分测试 |
| DRM 解密 | 🟢 低 | 已有纯 Go 替代 | `mp4ff-decrypt` 替代 mp4decrypt/shaka-packager |
| TS→MP4 remux | 🟢 低 | gomedia 成熟稳定 | 纯 Go 实现，无外部工具依赖 |
| 事件回调性能 | 🟢 低 | 高频进度回调可能阻塞下载 | 使用带缓冲 channel + 非阻塞发送 |

### 8.2 架构风险

| 风险项 | 说明 | 缓解方案 |
|--------|------|---------|
| 接口设计过度 | SDK 接口太复杂，消费者难用 | 先提供最简 API（GetStreams + Download），高级功能通过 Options 扩展 |
| 原项目跟进 | 原项目持续更新，Go 版本需同步 | 核心逻辑自研，不追求 1:1 复制，关注协议规范 |
| 错误传播 | goroutine 内错误如何传递给消费者 | 事件系统中包含 ErrorEvent，Download() 返回最终错误 |

---

## 九、与原项目调用方式对比

| 维度 | 调用原 exe | Go SDK |
|------|-----------|--------|
| 进度获取 | 解析 stdout（脆弱） | 事件回调（结构化） |
| 并发管理 | 每个任务一个进程 | goroutine 池（轻量） |
| 取消控制 | kill 进程 | context 取消（优雅） |
| 错误处理 | 解析 stderr | error 返回 + 事件 |
| 流选择 | 交互式 stdin | API 调用（编程式） |
| 跨平台 | 需要 .NET 运行时 | 单二进制 |
| 嵌入性 | 无法嵌入 | 直接 import |
| TS→MP4 | 依赖外部 ffmpeg | 纯 Go（gomedia） |
| DRM 解密 | 依赖外部 mp4decrypt | 纯 Go（mp4ff-decrypt） |
| 外部工具 | ffmpeg + mp4decrypt + ffprobe | 仅 ffmpeg 可选（Dolby Vision） |

---

## 十、结论

| 维度 | 评估 |
|------|------|
| **技术可行性** | ✅ 完全可行，Go 标准库 + 3 个纯 Go 库覆盖所有核心需求 |
| **工作量** | 📊 ~5,560 行 Go 代码，~32 个工作日 |
| **外部依赖** | 📦 3 个纯 Go 库（无 CGO），仅 Dolby Vision 需要外部 ffmpeg |
| **收益** | 🎯 纯 Go SDK，任意前端框架可接入，彻底解耦 |
| **风险** | ⚠️ DASH 解析复杂度较高，直播录制需要充分测试 |
| **建议** | ✅ **推荐执行**，从 Phase 1 开始，2 周内可出可用 SDK |

**核心价值：一次编写，到处调用。** 不管以后用 Wails、Electron、Flutter、还是纯 CLI，同一个 Go SDK 直接用，不用再改下载引擎的代码。

**对比原项目的关键优势：**
- 原项目依赖 .NET 运行时 + ffmpeg + mp4decrypt → Go 版本仅需单二进制（ffmpeg 可选）
- 原项目通过 stdout 解析进度 → Go 版本事件回调，结构化数据
- 原项目每个下载任务一个进程 → Go 版本 goroutine 池，内存占用低一个数量级
