# v0.2.0 发布与升级说明

## 发布目标

用户已确认：将 FFmpeg 默认合并后端发布到 GitHub main，并创建不可变版本标签 v0.2.0，供其他 Go 项目按版本引用。

仓库：github.com/lullabyable/GOm3u8DL。发布仅包含源码、测试和文档，不上传本地 exe、FFmpeg 二进制、媒体样本、下载目录、诊断日志或凭据。不强制推送，不移动已有版本标签。

## 升级

```bash
go get github.com/lullabyable/GOm3u8DL@v0.2.0
go mod tidy
```

SDK 导入路径保持不变：

```go
import "github.com/lullabyable/GOm3u8DL/pkg/m3u8dl"
```

CLI 可以从源码安装：

```bash
go install github.com/lullabyable/GOm3u8DL/cmd/m3u8dl@v0.2.0
```

Go 最低版本由 go.mod 声明为 1.22。本机回归环境是 Go 1.26.2/windows-amd64；尚未据此宣称已在全部 Go 版本和平台完成运行验证。

## 主要改动

- Go 下载和解密保持独立，默认使用 FFmpeg stream copy 重封装 MP4/混流，不默认转码。
- 为 fMP4 按轨准备 init + 有序媒体分片，支持分离音视频。
- context 取消、机器可读进度、结构化错误与诊断日志。
- 成功前不发布半成品，失败保留分片，拒绝覆盖已有最终文件。
- 新增 Engine.MergeDownloadedWithFFmpeg，可复用已有下载结果重试合并。
- 保留纯 Go 合并选项并标记为实验性。
- 修正两处原有测试的 Windows 路径及空任务计时假设，不改变下载业务逻辑。
- CLI 默认版本显示与标签统一为 0.2.0；此前源码中的 1.0.0 为硬编码展示值，远程没有相应版本标签。

## 升级注意事项

1. 默认运行时需要 FFmpeg。加入 PATH，或通过 DownloadRequest.FFmpegPath / CLI -ffmpeg-dir 指定目录或可执行文件；程序不自动下载、安装或分发 FFmpeg。
2. MergeMode 零值 0 从旧 binary 改成 default/FFmpeg。显式 MergeModeBinary 的数值改为 5；TS2MP4=1、FMP4=2、FFmpeg=3、No=4 不变。持久化旧整数 0 且希望 binary 的消费者需要迁移。
3. 现有配置显式写了 ts2mp4/fmp4/binary 时继续生效；需要改为 ffmpeg 或通过 CLI 覆盖。
4. ProgressEvent.Phase 非空时为合并阶段；Percent=-1 表示未知。消费者不要继续将其当作下载字节进度。
5. 合并取消后重新执行合并，不支持续写中间 MP4；原始分片保留。
6. 不自动转码不兼容 MP4 的编码；错误的清单、分片、解密或配置切换仍可能失败。
7. 分发 FFmpeg 的许可义务需按实际构建独立评估。本次只发布 Go 项目源码，不捆绑 FFmpeg。

详细后端行为与测试范围见 docs/mcp-ffmpeg-backend.md；原纯 Go 实现审查见 docs/mcp-mp4-merge-audit.md。

## 发布前验证与发布策略

- 上一轮完整 go test ./... -count=1、go vet ./... 和 Windows 构建已通过。
- 本次发布准备已再次通过 go test ./... -count=1、go vet ./...、Windows 构建及 git diff --check；新版 CLI 的 -version 输出为 0.2.0。
- 使用正常 fast-forward 推送。若远程 main 在发布期间变化，停止覆盖并重新检查，不使用 force。
- 标签推送后核对远程 main 与 v0.2.0 指向同一发布提交，再验证按标签下载 Go 模块。
- 标签发布不等同于上传 GitHub Release 二进制附件；本次发布目的是供 Go 模块消费者引用源码版本。
