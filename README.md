# julia-client

Persistent Julia REPL client and daemon.

Runs Julia code in a long-lived session over a Unix socket so that state (variables, loaded packages) survives between calls. The project environment is auto-detected from `$PWD`.

## Installation

**Pre-built binary** (no Go required) — download from [GitHub Releases](https://github.com/Beforerr/julia-client/releases/latest), extract, and put `julia-client` on your `$PATH`.

**From source** (requires Go 1.22+):

```bash
go install github.com/Beforerr/julia-client/go@latest
```

Requires Julia on `$PATH`. The single binary acts as both client and daemon (daemon auto-starts on first `eval`).

## Usage

```bash
# Evaluate code (daemon starts automatically)
julia-client -e 'println("hello")'

# Pkg operations (disable timeout)
julia-client --timeout 0 -e 'using Pkg; Pkg.add("Example")'

# Explicit project environment
julia-client --project /path/to/project -e 'using MyPackage'

# Read from stdin
echo 'println("hello")' | julia-client

# Session management
julia-client sessions   # list active sessions
julia-client restart    # restart current session
julia-client stop       # shut down the daemon
```

## Claude Code skill

The included skill at `skills/julia-client/SKILL.md` teaches Claude Code how to use `julia-client`. Install it by adding this repo's `skills/` directory to your Claude Code skill search paths.

## Architecture

A single `julia-client` binary serves as both client and daemon:

- **Client mode** (default) — sends JSON requests over a Unix socket (`~/.local/share/julia-client/julia-daemon.sock`)
- **Daemon mode** (`julia-client daemon`) — background server managing persistent Julia processes; auto-started on first `eval`, shuts down after 30 minutes of inactivity

## Alternatives

- [julia-mcp](https://github.com/aplavin/julia-mcp?tab=readme-ov-file) is very similar but uses MCP server instead
- [DaemonicCabal.jl](https://github.com/tecosaur/DaemonicCabal.jl) only runs on Linux