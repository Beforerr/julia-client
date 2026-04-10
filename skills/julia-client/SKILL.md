---
name: julia-client
description: "Run Julia code in a persistent session with project env auto-detection and timeout handling. Use for efficient Julia code execution."
---

## Running code

```bash
julia-client -e 'x=1'
julia-client -e 'println(x)'

# Long-running tasks (pkg install, compile, heavy compute): set longer timeout or disable
julia-client --timeout 300 -e 'include("heavy_script.jl")'
julia-client --timeout 0 -e 'using Pkg; Pkg.add("Example")'
```

## Session management

```bash
julia-client sessions   # list active sessions
julia-client restart    # restart session (slow, loses state; use if "Julia session has died")
julia-client stop       # shut down the daemon
```
