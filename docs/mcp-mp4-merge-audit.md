# MP4 分片合并故障检查

## 范围与结论边界

- 检查对象：当前 ShunCode 工作区 GOm3u8DL 的下载、TS 重封装、fMP4 合并和音视频混流源码。
- 本次是静态审查，未运行测试、构建、影片下载或播放器验证，未修改业务源码。
- downloads 目录的常规目录枚举为空，常规文件搜索未发现日志；没有用户失败影片的分片、清单、错误输出，因此下述为代码缺陷及其触发条件，不代表已复现某一部影片的故障。
- 检查前 git status --short 无输出。当前源码与用户实际运行的 m3u8dl.exe 是否一致，尚未核实。
- 结论：合并逻辑存在多处输入相关的确定性缺陷，不应归因于 Go 本身或简单认定下载顺序错乱。并发下载结果按索引归位，见 pkg/downloader/manager.go:178-200。

## 1. P0：PES 缓冲区复用原始 TS 数组，交错 PID 可相互覆盖

位置：pkg/merge/ts2mp4.go:747-775、837-863。

parseTSPacket 接收 data[offset:offset+188]，这是两下标切片，其容量仍可延伸至整个文件末尾。pkt.payload = data[payloadOffset:] 继续共享该数组；随后：

```go
buf.data = pkt.payload
buf.data = append(buf.data, pkt.payload...)
```

当容量足够时，append 在原始文件数组内写入，而不是创建独立 PES 缓冲区。尚未提取的其他 PID 的 PES 也引用同一数组。

静态推演：视频 PES 起始包 V1、音频 PES 起始包 A1、视频续包 V2 按此顺序出现，均为 payload-only 的 188 字节包。V1 的 payload 从相对偏移 4 开始，长度 184、容量可延伸到文件末尾。追加 V2 的 184 字节到视频缓冲区时，从相对偏移 188 写起，覆盖 A1 的 TS 头和大部分 payload；音频缓冲区对 A1 payload 的引用也随之损坏。

影响：音视频包交错方式不同，会出现样本丢失、错误 PES 数据、无声或解码损坏。此处是共享内存所有权错误，不是并发调度问题。

修复方向：PES 缓冲区独立拥有数据，首次写入复制 payload，或采用严格限制容量的不可变输入视图和独立累积缓冲区。检查所有已保存 PES 的生命周期。

## 2. P0：视频时间表实际常按 25fps 生成，且缺失 B 帧合成时间

位置：pkg/merge/ts2mp4.go:788-791、933-1069、470-527、531-552。

- parseTSFile 对每个 PES 单独调用 extractSamplesFromPES。
- 每个视频 PES 整体生成一个 sample，duration 固定初始化为 3600。
- 修正时长只在该次调用返回多个 sample 时发生；常见的一 PES 一帧输入，函数只有一个 sample，无法计算与下一 PES 的时间差。
- 默认 timescale=90000，3600 等于 40ms，即 25fps。24fps 应约为 3750，30fps 应约为 3000。
- 即便修正分支执行，使用的是 PTS 差，而 stts 表达解码时间，应依据 DTS 构建。
- stbl 只写 stsd/stts/stss/stsc/stsz/stco，没有 ctts，采集的 PTS-DTS 差没有表达进普通 MP4。
- 视频 PES 不等于访问单元：一个 PES 包含多个访问单元，或一个访问单元跨 PES 时，当前直接一 PES 一 sample 的规则也不成立。

影响：非 25fps、含 B 帧、可变帧率或非常规 PES 分组的影片出现时长错误、变速、卡顿、音画不同步；函数仍可能返回成功。

修复方向：按编码规则组装访问单元，跨 PES/分片维护 DTS/PTS，基于 DTS 生成 stts，以 PTS-DTS 生成 ctts（必要时支持有符号偏移）；保留音视频起始偏移，明确时间戳回绕和断点策略。

## 3. P0：分离音视频 fMP4 的 tkhd track_ID 写错位置

位置：pkg/merge/mux.go:309-344、374-437。

rewriteTrakID 不检查 tkhd version，固定将新 ID 写到 tkhd body 偏移 20。

- version 0：track_ID 位于 body 偏移 12；偏移 20 是 duration。
- version 1：track_ID 才位于 body 偏移 20。

与此同时 tfhd 被改为视频 1、音频 2。常见两个输入轨均为 ID=1 且 tkhd version=0 时，moov 中两个 track_ID 仍为 1，音频 fragment 的 tfhd 却为 2，并把轨道 duration 改成 1/2。

影响：DASH/fMP4 分离音视频输入可能合并后丢失音轨、轨道映射失败或播放异常；是否触发取决于原始轨道 ID 和 tkhd 版本。

其他混流问题：

- pkg/merge/mux.go:323-371 重建 trex 时没有保留原来的默认时长、大小和 sample flags；依赖原 trex 默认值、未在 tfhd/trun 显式声明的片段会被错误解释。
- pkg/merge/mux.go:165-196 忽略媒体数据 out.Write 的返回错误，磁盘写失败也可能返回 nil。

修复方向：按 box version 解析字段，统一重映射轨道相关引用，保留 trex 默认值；所有写入错误必须上报。

## 4. P1：最后一个 TS 分片的空编码配置覆盖前面有效配置

位置：pkg/merge/ts2mp4.go:32-44、780-834、1814-1834、1096-1112。

- 每个分片重新创建 avcConfig/hevcConfig/aacConfig。
- 每次解析都返回非 nil 的 trackInfo。
- 顶层循环无条件使用最后一个分片的 trackInfo。
- 最后一个分片没有 SPS/PPS/VPS 时，前面有效配置被空值覆盖。
- H.264 兜底从前 10 个 sample 查找 SPS/PPS，但 sample 已在 1014 行转换为 AVCC 长度前缀格式，而 extractSPSPPS 仅调用 Annex-B 的 findNALUnits；兜底与输入格式不匹配。HEVC 提取器有 AVCC fallback，不应把两者混为一谈。
- 最终可能写入空 avcC。每 PES 内部单独收集参数集，也无法可靠处理 SPS/PPS 分散在不同 PES 的情况。

影响：每片都重复参数集的影片较容易成功；仅开头携带参数集、尾片缺参数集的影片可能黑屏、无法解析或分辨率错误。

修复方向：在整条流生命周期缓存参数集，空值不覆盖有效值；明确支持配置更新和多 sample description，不能仅保留任意最后一份配置。缺关键配置应明确失败。

## 5. P1：大于 4GiB 的块偏移被截断，且整部影片驻留内存

位置：pkg/merge/ts2mp4.go:27-45、143-175、602-607、677、989-990。

虽然 mdat 支持 64 位长度，但所有块偏移只写 stco，并强制 uint32(offset)。任何 sample 文件偏移超过 0xFFFFFFFF 时都会截断，应该使用 co64。支持大 mdat 不等于支持大 MP4。

此外所有分片的音视频 sample.data 在输出前都累计到内存中；解析期间还有文件、PES 和样本副本。内存规模随整部影片媒体数据增长。

影响：小影片能成功，长片/高码率片可能在后半段读到错误位置，或合并阶段高内存、OOM。OOM 是资源风险，本次未观测实际发生。

修复方向：根据最终偏移选择 stco/co64，并在表宽变化后重新计算 moov 大小和布局；采用流式/两遍处理或临时媒体存储，而非全片 payload 常驻内存。

## 6. P1：音频编码识别与实际写入不一致

位置：pkg/merge/ts2mp4.go:917-924、799-804、1025-1044、1872-1925。

PMT 接受 AAC(0x0f) 和 MPEG Audio(0x03/0x04)，但音频提取始终尝试 ADTS；没有提取到 AAC 时把原始 payload 当作 sample，输出仍使用 AAC 的 mp4a/esds。没有配置时还伪造默认 AudioSpecificConfig，channel_count 固定为 2。

AC-3/E-AC-3、AAC LATM 等常见其他 stream_type 不在当前识别分支中，可能不生成音轨而不报不支持。不是所有 mp4a 都等于 AAC，但当前 esds 按 AAC 构建，因此 MPEG Audio 路径仍是错误封装。

影响：AAC-LC 常见输入较容易成功，其他音频编码、多声道或不同 AAC 配置可能无声、漏轨或解码错误。

修复方向：建立明确的编码支持矩阵，不支持的编码返回结构化错误，不静默改标签或伪造配置；按实际 codec 组帧并写对应 sample entry。

## 7. P1：分片间上下文和 discontinuity 没有得到处理

位置：pkg/parser/hls/extractor.go:185-199、227-228；pkg/merge/ts2mp4.go:693-737、772-775。

- EXT-X-DISCONTINUITY 仍为 TODO。
- 多个 EXT-X-MAP 只保留最后一个 MediaInit，而不是按分段绑定配置。
- TS 每片重新建立 PAT/PMT 和 PES 状态；缺失表时直接猜 PID 256/257。
- 片尾缓冲直接输出，下一片的非起始续包不能衔接前一片。

影响：时间轴重置、广告拼接、配置切换、需要跨片延续的输入，可能出现错轨、丢帧、缺配置和时间异常。对于每片完全自包含的 TS 不一定触发跨片 PES 问题。

修复方向：以连续时间段为单位维持 demux 状态，明确处理 discontinuity、初始化段切换、PTS/DTS 回绕和参数更新，不用猜测 PID 替代解析错误。

## 8. P1：fMP4 单流路径只是复制，并未完整重定位

位置：pkg/m3u8dl/engine.go:358-365；pkg/merge/fmp4.go:12-40、43-88、123-176。

引擎调用 FMP4Merge，把 init 和各 media segment 原样拼接。这对于同一初始化配置、连续时间轴、正确使用相对寻址的 fMP4 可以成立；不能笼统称所有 fMP4 拼接都错误。

对于需要调整绝对 base_data_offset、初始化配置变化等输入，当前逻辑不会修正或拒绝。不能简单改调 FMP4MergeWithRewrite：该辅助实现主要改序号，未真正实现完整偏移重定位，rewriteMoof 的子 box 相对偏移还缺少外层 moof header，rewriteTraf:172 用 Uint32 读取 3 字节切片，若进入该分支会 panic。这是辅助路径的潜在问题，不是当前引擎单流默认调用的直接 panic 来源。

修复方向：先解析并验证寻址模式、轨道配置和时间连续性，再执行正确重定位；解析失败必须显式返回，不能原样写入后标成功。

## 9. 输入检查与错误状态的附加问题

- pkg/downloader/segment.go:94-121 未应用 DownloadRequest.Headers；需要 Referer/Cookie/Authorization 的媒体请求可能与清单请求结果不同。响应检查只验证状态码，不验证是否真为媒体。
- Range 请求同时接受 200/206，未校验 Content-Range 或 ExpectLength；服务端忽略 Range 时可能把整文件当每个分片。
- pkg/m3u8dl/engine.go:132-141 获取子播放列表后仍复用 master 的 HLS extractor；相对分片 URL 可能基于错误目录解析。这是下载输入问题，不是 MP4 写入本身。
- pkg/m3u8dl/engine.go:272-274 的清理 defer 在合并失败时也执行；开启 DelAfterDone 后失败分片可能被删除，妨碍重试与诊断。
- pkg/m3u8dl/engine.go:685-720 只读 32 字节，却尝试检查相隔 188 字节的同步字节，尾部扫描分支不可能满足该条件。
- TS2MP4Remux 和混流接口未接收 context、没有合并细粒度进度；下载可控不代表合并阶段已支持取消与完整状态控制。

## 为什么现有测试不能证明真实影片合并正确

本次仅阅读测试，没有执行。

pkg/merge/merge_test.go:575-667 的关键合并用例使用 buildMinimalTS 构造的极简数据，主要检查 ftyp、mdat 首偏移和 stco 条目数。它们不是对真实影片的帧数、DTS/PTS、音画同步和可解码性验证；通过这些断言不能排除上述缺陷。未据此断言整个仓库完全没有其他相关测试。

## 建议修复顺序（保持纯 Go，不切换外部二进制）

1. 先修数据正确性：PES 缓冲区所有权、tkhd 版本偏移、写错误传播、失败时保留分片。
2. 再修时间轴：访问单元组装、跨片 DTS/PTS、stts/ctts 和音视频起始偏移。
3. 建立整流 codec/初始化状态与严格的支持检查，修复末片覆盖和 discontinuity。
4. 支持 co64、正确的 fMP4 寻址和 trex 默认值，改为有界内存处理。
5. 合并接口接入 context、分阶段进度和结构化错误，符合全 Go 控制状态的目标。

## 要锁定用户具体影片还需要什么

提供一个失败任务的现有文件路径和完整错误信息即可，优先：

- 原始 m3u8/MPD 清单，以及实际选择的音视频轨道；敏感令牌可脱敏。
- 已下载分片所在目录（暂勿删除），尤其首片、尾片、异常位置前后分片与 init。
- 失败形式：返回合并错误 / 程序退出 / 输出 MP4 黑屏无声 / 时长或同步错误。
- 输出大小、合并模式、实际运行的 exe 来源或构建版本。

当前没有证据把某一项标成该影片的唯一根因；上述代码问题可以直接进入修复，但应与实际失败样本关联后再确认故障闭环。
