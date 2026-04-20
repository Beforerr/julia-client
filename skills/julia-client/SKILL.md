---
name: julia-client
description: "Run Julia code with session state persistence, project env auto-detection and timeout handling. Use for efficient Julia code execution."
---

## Running code

```bash
julia-client -e 'x=1' # Evaluate
julia-client -E 'x' # Evaluate and display

# Long-running tasks (pkg install, compile, heavy compute): set longer timeout or disable
julia-client --timeout 300 -e 'include("heavy_script.jl")'
julia-client --timeout 0 -e 'using Pkg; Pkg.add("Example")'
```

## Tips

- Only run setup (e.g. `Pkg.activate`, `using`) once per session.
- Prefer `using TestEnv; TestEnv.activate(); include("test/runtests.jl")` over `Pkg.test()`

## Session management

```bash
julia-client sessions   # list active sessions
julia-client restart    # restart session (slow, loses state; use if "Julia session has died")
julia-client stop       # shut down the daemon
```
