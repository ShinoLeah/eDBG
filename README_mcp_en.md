# eDBG MCP

This document focuses on eDBG's MCP mode and the host-side installer workflow.

## Overview

- Phone-side `eDBG` exposes an HTTP MCP server
- The host forwards the port with `adb forward`
- The host-side `edbg-mcp-install` writes MCP configuration into AI clients
- The installer is a standalone host utility and is not shipped inside the Android `eDBG` binary

Default MCP URL:

```text
http://127.0.0.1:19810/mcp
```

## Start On The Device

```shell
adb push eDBG /data/local/tmp
adb shell
su
chmod +x /data/local/tmp/eDBG
/data/local/tmp/eDBG --mcp
```

If `--mcp-port` is not specified, eDBG listens on `19810`.

Forward the port on the host:

```shell
adb forward tcp:19810 tcp:19810
```

## MCP Runtime Behavior

- `--mcp` forces `-prefer uprobe -show-vertual`.
- No startup breakpoint is installed automatically
- eDBG starts in standby mode and does not launch the target app by itself
- In the initial standby state there is no selected target yet, so the first safe step is `attach(package, library)`
- After a target is selected but before the app is launched, only `attach`, `break`, `info_break`, `info_file`, breakpoint management, and `run` are allowed
- The MCP `break` tool always interprets the offset as a virtual offset and maps internally to `vbreak`
- `attach` selects the current `package` and `library`
- `run` directly launch the attached target app with `am start`
- `continue` blocks until a breakpoint is actually hit
- `quit` only resets the current MCP context and returns to the initial standby state; it does not stop the MCP server

Recommended flow:

```text
1. Start eDBG: /data/local/tmp/eDBG --mcp
2. Forward the port: adb forward tcp:19810 tcp:19810
3. Call attach(package, library) to select the target
4. Call break to add virtual-offset breakpoints
5. Call run to launch the app
6. Call continue and wait for a breakpoint hit
```

## Building The Installer

The repository now includes a dedicated installer makefile:

```shell
make -f Makefile_installer current
make -f Makefile_installer all
```

Artifacts are written to:

```text
bin/
```

By default, it builds installer binaries for these mainstream desktop targets:

- `darwin_amd64`
- `darwin_arm64`
- `linux_amd64`
- `linux_arm64`
- `windows_amd64`
- `windows_arm64`

## Installer Usage

```shell
./bin/edbg-mcp-install --list-clients
./bin/edbg-mcp-install --install
./bin/edbg-mcp-install --install --clients codex,cursor,claude
./bin/edbg-mcp-install --project --install --clients cursor,vscode,zed
./bin/edbg-mcp-install --config
```

If you change `--mcp-port`, or use a different local forwarded port:

```shell
./bin/edbg-mcp-install --install --url http://127.0.0.1:23456/mcp
```

To remove installed config:

```shell
./bin/edbg-mcp-install --uninstall
./bin/edbg-mcp-install --uninstall --clients codex,cursor
```

## Supported AI Clients

You can always inspect the live list with:

```shell
./bin/edbg-mcp-install --list-clients
```

The current implementation covers major clients such as:

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

## Notes

- The installer writes JSON or TOML depending on each client's config format
- Use `--project` for clients that support project-level MCP configuration
- Global installation only updates clients whose config directory already exists, to avoid polluting unrelated environments
- `Codex` is written into `~/.codex/config.toml`
