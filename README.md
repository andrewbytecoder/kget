Pget (KCP Edition) - Resumable downloader over KCP
=======

This repository has been refactored into a **KCP-only** resumable downloader and file server.

## Description

`pget` downloads a file from a `pgetkcpd` server over **KCP**, with:

- **Resumable**: interruption-safe (partial files are reused)
- **Parallel**: split into N parts (`--procs`) and downloaded concurrently
- **KCP only**: no HTTPS / HTTP client code in this project
- **Cross-platform**: Go (Windows / Linux / macOS)

## Quick start

### 1) Start server

Serve files under `--root` (client paths are relative to this root):

```bash
./pgetkcpd serve --listen :29900 --root .
```

### 2) Download from client

Download `--path` from the server, with 4 parallel parts, output to current directory:

```bash
./pget get --kcp 127.0.0.1:29900 --path "pgetkcpd" -p 4 -o .
```

### 3) Resume

If you interrupt the download (Ctrl+C), run the same command again. The downloader will continue from the existing partial files in `_<filename>.<procs>/`.

## Installation

### Go

```bash
go install ./cmd/pget@latest
go install ./cmd/pgetkcpd@latest
```

## Synopsis

### Client

```bash
pget get --kcp <host:port> --path <remote-path> [-p <procs>] [-o <output>]
```

Examples:

```bash
pget get --kcp 127.0.0.1:29900 --path "main.go" -p 4 -o .
pget get --kcp 10.0.0.2:29900 --path "releases/app.zip" -p 8 -o "./downloads/"
```

### Server

```bash
pgetkcpd serve --listen <host:port> --root <dir>
```

Example:

```bash
pgetkcpd serve --listen :29900 --root /srv/files
```

## Logging

Both client and server support `--log-level debug|info|warn|error`.

```bash
pgetkcpd serve --listen :29900 --root . --log-level debug
pget get --kcp 127.0.0.1:29900 --path "pgetkcpd" -p 4 -o . --log-level debug
```

## Tuning (environment variables)

- `PGET_KCP_IO_TIMEOUT_MS`: sliding per-read deadline in milliseconds (default 20000)
- `PGET_KCP_CHUNK_SIZE`: bytes per range request (default 4194304)

Examples:

```bash
PGET_KCP_IO_TIMEOUT_MS=60000 PGET_KCP_CHUNK_SIZE=1048576 pget get --kcp 127.0.0.1:29900 --path "pgetkcpd" -p 4 -o .
```

## Notes

- The downloader writes partial files into `_<filename>.<procs>/` and merges them after all parts complete.
- If older buggy partial files exist and are oversized, the client will truncate them during resume and will only copy the expected bytes during final merge.

## Binary

Build from source (this repo is a local refactor):

```bash
go build ./cmd/pget
go build ./cmd/pgetkcpd
```

## Author
