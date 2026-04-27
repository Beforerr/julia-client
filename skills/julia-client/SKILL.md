---
name: julia-client
description: "Run Julia code with session state persistence, project env auto-detection and timeout handling. Use for efficient Julia code execution."
---

## Running code

```bash
julia-client -e 'const x=1' # Evaluate
julia-client -E 'x' # Evaluate and display
julia-client --fresh -E 'x=2' # Run with clean session state

# Long-running tasks (pkg install, compile, heavy compute): set longer timeout or disable (0)
julia-client --timeout 300 heavy_script.jl
```

## Tips

- Only run setup (e.g. `Pkg.activate`, `using`) once per session.

## Session management

```bash
julia-client sessions   # list active sessions
julia-client stop       # shut down the daemon
```
