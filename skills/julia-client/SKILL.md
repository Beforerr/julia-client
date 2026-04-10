---
name: julia-client
description: "Run Julia code in a persistent session with project env auto-detection and timeout handling. Use for efficient Julia code execution."
category: language
complexity: basic
---

## Running code

```bash
julia-client eval 'x=1'
julia-client eval 'println(x)'
```

- Use `display(...)` or `println(...)` to produce visible output.
- For Pkg operations, disable the timeout:
  ```bash
  julia-client eval --timeout 0 'using Pkg; Pkg.add("Example")'
  ```
- For a custom Julia binary:
  ```bash
  julia-client eval --julia-cmd "julia +1.11" 'versioninfo()'
  ```

## Session management

```bash
julia-client sessions   # list active sessions
julia-client restart    # restart session (slow, loses state; use if "Julia session has died")
julia-client stop       # shut down the daemon
```
