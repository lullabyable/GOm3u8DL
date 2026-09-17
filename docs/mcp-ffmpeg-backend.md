# FFmpeg 默认合并后端

## 目标与分工

Go 继续负责清单解析、选轨、分片下载、重试、解密和任务编排。FFmpeg 只读取 Go 已下载的本地媒体数据，用 stream copy 进行 MP4 重封装/音视频混流，不把下载交回 N_m3u8DL-RE，也不进行默认转码。

本次没有宣称原有解析器、解密器和暂停/续传逻辑全部完善；这些模块的既有兼容性问题不会因为换合并器自动消失。

## 默认模式与兼容性

- CLI 参数默认值、交互模式默认值、DefaultConfig、配置缺省合并策略：ffmpeg。
- SDK DownloadRequest 的 MergeMode 零值现在为 MergeModeDefault，解析为 FFmpeg。
- 保留旧模式：ts2mp4/fmp4 为实验性纯 Go 路径，binary 为原始字节拼接，no 仅下载。
- 枚举迁移：原数值 0 的 binary 改成 5；0 现在为 default。TS2MP4=1、FMP4=2、FFmpeg=3、No=4 不变。使用符号常量的调用者重新编译即可；持久化过旧数值 0 且希望继续 binary 的调用者需要迁移为 5。建议对外保存模式名称，不保存整数。
- 现有配置文件显式指定的旧合并模式仍然生效，不擅自覆盖用户偏好。请修改为 `"merge": "ffmpeg"` 或显式传 `-merge ffmpeg`。
- 保留 FFmpegMerge、MuxToMP4、FFmpegMuxAV 兼容函数；它们没有调用者 context 参数。新的任务代码使用 RemuxFFmpeg 或 MergeDownloadedWithFFmpeg。

## 入口与文件

- pkg/merge/ffmpeg.go：共享可执行文件发现和旧 API 兼容包装。
- pkg/merge/ffmpeg_backend.go：context 管理的子进程、任务输出锁、诊断日志、临时输出、校验和提交。
- pkg/merge/ffmpeg_input.go：本地输入准备、有界缓冲拼接、MP4 结构校验。
- pkg/merge/ffmpeg_progress.go：机器可读进度解析和有界 stderr 尾部。
- pkg/m3u8dl/merge.go：SDK 的独立合并/重试入口与状态适配。
- pkg/m3u8dl/merge_cleanup.go：成功清理保护，拒绝删除包含最终输出的目录。
- pkg/m3u8dl/engine.go：默认后端、失败/取消事件、成功后清理。
- cmd/m3u8dl/main.go、cmd/m3u8dl/merge_progress.go：CLI 默认值、分离音视频调用、阶段进度、信号取消。

## 输入策略

### TS / 独立媒体文件

单个独立媒体文件直接交给 FFmpeg。多个 TS/独立媒体分片通过本地 ffconcat 清单交给 concat demuxer，使其按分段处理时间轴，而不是将所有时间戳重置的文件直接做裸字节拼接。

清单使用绝对路径、单引号转义和平台路径规范化，拒绝换行/NUL 路径。输入协议白名单仅包含 file，不给 FFmpeg 远程 URL、下载凭据或 shell 命令。

### fMP4

输入带 InitPath 时，使用固定大小缓冲区拼接：init 一次 + 顺序排列的媒体分片，生成该轨的可寻址临时 MP4 输入。不会把缺 init 的 moof 当独立 MP4 文件逐个交给 concat demuxer。

双输入时分别准备视频和音频，每一轨都有自己的 init 和分片列表；然后明确映射视频输入的第一个视频轨和音频输入的第一个音频轨。单输入保留该输入中 FFmpeg 可映射的音视频轨。当前不自动混入字幕、附件和数据轨。

内存不再累计整部影片 payload，但 fMP4 准备阶段需要额外磁盘空间存放连续输入，重封装时还需要完整输出空间。

## FFmpeg 调用与任务状态

- 使用 os/exec.CommandContext，不通过 shell 拼接命令，不在 SDK 中交互式提示安装。
- 使用 `-nostdin -progress pipe:1 -nostats -c copy -movflags +faststart -f mp4`。
- 使用 `-xerror`，遇到 FFmpeg 认定的错误时失败，不静默输出损坏结果；不保证所有异常都会被检测到。
- CLI 的 SIGINT/SIGTERM 先取消 context，让合并函数终止子进程并回收工作目录，而不是在取消信号处理器中立即 os.Exit。
- SDK 状态使用已有 TaskStatusMerging / Done / Failed / Cancelled。失败 StatusEvent.Error 携带错误。
- ProgressEvent 新增 Phase、MediaTime、MediaDuration、MergeSpeed；Phase 非空表示合并进度，不是下载字节进度。
- 阶段：preparing、remuxing、finalizing、done。
- 百分比根据 FFmpeg out_time_us / 清单媒体时长估计；未知时为 -1，提交前最多 99.9%。收到 FFmpeg progress=end 不直接表示任务完成，成功提交后才发 done/100。
- 回调同步执行，调用者应保持快速、不 panic；不要阻塞进度读取。跨 UI 线程时由调用者调度。

## 输出安全与诊断

1. 在输出目录创建目标专属 .merge.lock，防止本程序同时合并同名目标。
2. 目标已存在时拒绝覆盖，要求换保存名称。
3. 在输出目录创建私有 .m3u8dl-merge-* 工作目录，FFmpeg 只写该目录中的 output.mp4。
4. 进程成功退出、context 未取消且结构检查通过后，才 rename 到最终路径。提交前再次检查目标，锁只协调本程序，不宣称能防止任意外部程序同时创建文件的所有竞态。
5. 结构检查确认非空 ftyp/moov/mdat 和合法顶层 box 边界；这不是完整解码或音画同步验证。
6. 失败/取消不发布半成品，工作目录正常回收，原始下载分片保留；已存在的目标不改动。
7. 日志保存在输出目录 `输出文件名.ffmpeg-随机值.log`，成功和失败都保留。FFmpegError 提供 Stage、LogPath、Stderr 尾部和 Err，可用 errors.As/Is 识别。
8. 仅在合并成功且 DelAfterDone=true 时删除下载临时目录。输出被放进临时目录时拒绝清理并警告，避免删除成品。

强制杀死整个 Go 进程、断电等情况可能留下锁或工作目录。确认没有对应任务运行后再人工清理，不自动删除可能属于仍在执行任务的锁。

## CLI 使用

```powershell
.\m3u8dl-ffmpeg.exe -url "https://example.com/index.m3u8" -merge ffmpeg -ffmpeg-dir "C:\tools\ffmpeg\bin"
```

FFmpeg 已在 PATH 时可省略 ffmpeg-dir。仅下载不需要 FFmpeg：

```powershell
.\m3u8dl-ffmpeg.exe -url "https://example.com/index.m3u8" -merge no
```

希望成功后也保留分片，显式使用 `-del-after-done=false`。原有配置中的 false 现在也能覆盖 CLI 缺省 true。

## SDK 合并和失败后重试

```go
// 使用 DownloadOnly 获取分片，失败后保留这些结果供再次合并。
video, err := engine.DownloadOnly(ctx, videoRequest, handler)
if err != nil { return err }
audio, err := engine.DownloadOnly(ctx, audioRequest, handler)
if err != nil { return err }

err = engine.MergeDownloadedWithFFmpeg(ctx, model.DownloadRequest{
    OutputDir: "./output",
    SaveName: "movie",
    FFmpegPath: "", // 查找 PATH；也可传目录或可执行文件
    DelAfterDone: false,
}, []m3u8dl.DownloadResult{*video, *audio}, handler)
```

只有一个音视频混合流时传一个 DownloadResult。失败后可修正环境/输入，用新的 context 再次调用，不必重新下载；若进程已退出，调用者应持久化有序 SegmentPaths、InitPath、Playlist 和临时目录信息，以便重建参数。本次没有新增 CLI 自动扫描旧分片目录的功能。

## 明确限制

- 合并取消后通常从头重新合并，不提供 FFmpeg 中间 MP4 的断点续写。没有实现跨平台无损暂停/恢复 FFmpeg 的保证。
- 不将不能存入 MP4 的编码自动转码；stream copy 无法封装时明确报错。
- fMP4 输入要求 init 与分片匹配、寻址与时间轴能构成可解析的连续输入。不同 init 切换、discontinuity、加密残留、上游错误 URL/Range 等仍需修复解析和下载层。
- 本次未修复原纯 Go remux 的已审查缺陷，只将它从默认路径移走并标记为实验性。
- 完整日志可能含本地路径和媒体诊断信息，分享前应检查脱敏。
- 未安装/捆绑 FFmpeg，不变更系统 PATH。分发时检查实际 FFmpeg 构建的 LGPL/GPL、专利及适用许可要求。

## 验证

已补充自动化覆盖：

- 子进程取消及 context 错误传播。
- 失败日志、结构错误、源分片保留、半成品不发布。
- 既有输出保护、工作目录和锁回收。
- init + 分片拼接、特殊路径、分块进度解析、有界错误尾部。
- 使用真实 FFmpeg 生成 24fps/B 帧的独立视频 fMP4 和 AAC 音频 fMP4，执行多分片混流并解码验证两轨；同时验证单轨 fMP4。
- SDK 默认模式、取消状态、输出目录清理保护。
- 本地 HTTP 分片下载 → 默认 FFmpeg 合并 → 解码检查；以及 HTTP 200 非媒体输入 → 失败并保留分片。

真实媒体测试仅使用本地生成的短样本，不代表用户之前失败的长片已经通过回归。此前未提供该影片的清单、分片和日志。

## 构建与回归记录

- 环境：Windows，Go 1.26.2，使用本机已有 FFmpeg；没有下载或安装依赖程序。
- `go vet ./...` 已通过，工作区诊断未返回错误或警告；`git diff --check` 无空白错误（Git 的 LF/CRLF 提示不是检查失败）。
- 已构建项目根目录 `m3u8dl-ffmpeg.exe`，原 `m3u8dl.exe` 未覆盖。执行新程序 `-h` 确认默认合并模式为 ffmpeg。
- 初次全量回归发现两个原有测试假设不适配 Windows：TestSegmentPath 使用写死的 POSIX 分隔符；TestTaskManagerSubmitAndWait 要求空任务耗时严格大于零。已分别改为 filepath.Join、允许空任务零测量时长，同时保留实际等待任务的正时长断言；下载业务源码未因此变更。
- 修正上述两处测试后，`go test ./... -count=1` 全量通过；真实 FFmpeg 集成用例在本机实际执行通过，未因缺少 FFmpeg 而跳过。
