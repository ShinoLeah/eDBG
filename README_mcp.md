# eDBG MCP

## 概览

- 手机端 `eDBG` 负责提供 HTTP MCP 服务
- 主机端通过 `adb forward` 转发端口
- 主机端 `edbg-mcp-install` 负责把 MCP 配置安装到各类 AI client
- installer 是独立的 host-side 工具，不运行在手机上，也不会打包进 Android 目标二进制

默认 MCP 地址：

```text
http://127.0.0.1:19810/mcp
```

## 使用

1. 下载最新 [Release](https://github.com/ShinoLeah/eDBG/releases) 版本和对应平台的 installer
2. 主机安装 mcp 工具：
```shell
./edbg-mcp-install --install
```
3. 推送到手机并运行 mcp 模式
```shell
adb push eDBG /data/local/tmp
adb shell
su
chmod +x /data/local/tmp/eDBG
./data/local/tmp/eDBG --mcp
```

如果没有指定 `--mcp-port`，默认监听 `19810`。
4. 本地转发监听端口：

```shell
adb forward tcp:19810 tcp:19810
```

## MCP 模式行为

- `--mcp` 模式下会强制使用 `-prefer uprobe -show-vertual`，阉割了硬件断点相关功能和单步相关功能
- 启动时不会预设任何启动断点
- 默认进入待命状态，不会主动启动 app
- 初始待命态下还没有目标；这时只应调用 `attach(package, library)` 选中目标
- 选中目标但尚未启动 app 时，只允许 `attach`、`break`、`info_break`、`info_file`、断点管理，以及 `run`
- MCP 暴露给 AI 的 `break` 内部始终按虚拟偏移处理，等价于 `vbreak`
- `attach` 设置当前 `package` 和 `library`
- `run` 直接启动当前目标 app，会执行 `am start`
- `continue` 会阻塞等待，直到真正命中断点才返回
- `quit` 只会清空当前 MCP 上下文并回到初始待命状态，不会退出 MCP server

## Installer 构建

仓库里提供了独立的 installer Makefile：

```shell
make -f Makefile_installer current
make -f Makefile_installer all
```

产物目录：

```text
bin/
```

默认会构建这些主流桌面平台版本：

- `darwin_amd64`
- `darwin_arm64`
- `linux_amd64`
- `linux_arm64`
- `windows_amd64`
- `windows_arm64`

## Installer 用法

```shell
./edbg-mcp-install --list-clients
./edbg-mcp-install --install
./edbg-mcp-install --install --clients codex,cursor,claude
./edbg-mcp-install --project --install --clients cursor,vscode,zed
./edbg-mcp-install --config
```

如果你改了 `--mcp-port`，或者把本地转发端口改成了别的值：

```shell
./edbg-mcp-install --install --url http://127.0.0.1:23456/mcp
```

卸载配置：

```shell
./edbg-mcp-install --uninstall
./edbg-mcp-install --uninstall --clients codex,cursor
```

## 当前支持的 AI Client

可通过命令查看：

```shell
./edbg-mcp-install --list-clients
```

当前实现覆盖的主流客户端包括：

- Claude
- Claude Code
- Cursor
- VS Code
- VS Code Insiders
- Codex
- Cline
- Roo Code
- Kilo Code
- Windsurf
- Zed
- Gemini CLI
- Qwen Coder
- Copilot CLI
- Amazon Q
- LM Studio
- Opencode
- Warp
- Kiro
- Trae
- Augment Code
- Qodo Gen

## 说明

- installer 会按不同 client 的配置格式分别写入 JSON 或 TOML
- 对支持 project-level MCP 配置的 client，可以使用 `--project`
- 全局安装默认只会修改已存在配置目录的 client，避免无意创建大量无关目录
- `Codex` 会写入 `~/.codex/config.toml`
