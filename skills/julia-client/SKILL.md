---
name: julia-client
description: "Run Julia code with session state persistence, project env auto-detection. Use for efficient Julia code execution, testing, and development."
---

## Running code

```bash
julia-client -e 'x=1' # Evaluate
julia-client -E 'x' # Evaluate and display

# Long-running tasks (pkg install, compile, plot, heavy compute): set longer timeout or disable timeout (0)
julia-client --timeout 300 heavy_script.jl
```

## Tips

- Run setup (e.g. `Pkg.activate`, `using PackageOnce`) once per session.
- Prefer `Revise` for automatically updating function definitions: only use `--fresh` flag when clean state is must.

## Session management

```bash
julia-client sessions   # list active sessions
julia-client stop       # shut down the daemon
```
