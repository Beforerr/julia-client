"""Unix socket server that manages persistent Julia sessions."""

import asyncio
import json
import os
import sys
import time
from pathlib import Path

from .session import SessionManager, DEFAULT_TIMEOUT, PKG_PATTERN

SOCKET_DIR = Path.home() / ".local" / "share" / "julia-client"
DEFAULT_SOCKET = SOCKET_DIR / "julia-daemon.sock"
DEFAULT_IDLE_TIMEOUT = 30 * 60  # 30 minutes

_manager: SessionManager | None = None
_last_request: float = 0.0
_stop_event: asyncio.Event | None = None


async def handle_request(data: dict) -> dict:
    global _last_request
    _last_request = time.monotonic()

    action = data.get("action")

    if action == "eval":
        code = data.get("code", "")
        env_path = data.get("env_path")
        timeout = data.get("timeout")
        julia_cmd = data.get("julia_cmd")

        if timeout is None:
            effective_timeout = None if PKG_PATTERN.search(code) else DEFAULT_TIMEOUT
        else:
            t = float(timeout)
            effective_timeout = t if t > 0 else None

        try:
            session = await _manager.get_or_create(env_path, julia_cmd=julia_cmd)
            output = await session.execute(code, timeout=effective_timeout)
            return {"output": output or "(no output)", "error": None}
        except RuntimeError as e:
            key = _manager._key(env_path)
            if key in _manager._sessions and not _manager._sessions[key].is_alive():
                del _manager._sessions[key]
            return {"output": None, "error": str(e)}

    elif action == "restart":
        await _manager.restart(data.get("env_path"))
        return {"output": "Session restarted.", "error": None}

    elif action == "sessions":
        sessions = _manager.list_sessions()
        if not sessions:
            return {"output": "No active Julia sessions.", "error": None}
        lines = []
        for s in sessions:
            status = "alive" if s["alive"] else "dead"
            label = f"{s['env_path']} (temp)" if s["temp"] else s["env_path"]
            julia = f" julia_cmd={s['julia_cmd']}" if "julia_cmd" in s else ""
            log = f" log={s['log_file']}" if "log_file" in s else ""
            lines.append(f"  {label}: {status}{julia}{log}")
        return {"output": "Active Julia sessions:\n" + "\n".join(lines), "error": None}

    elif action == "stop":
        _stop_event.set()
        return {"output": "Daemon stopping.", "error": None}

    elif action == "ping":
        return {"output": "pong", "error": None}

    else:
        return {"output": None, "error": f"Unknown action: {action!r}"}


async def handle_client(reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
    try:
        raw = await reader.readline()
        if not raw:
            return
        request = json.loads(raw)
        response = await handle_request(request)
        writer.write(json.dumps(response).encode() + b"\n")
        await writer.drain()
    except Exception as e:
        try:
            writer.write(json.dumps({"output": None, "error": f"Daemon error: {e}"}).encode() + b"\n")
            await writer.drain()
        except Exception:
            pass
    finally:
        writer.close()
        try:
            await writer.wait_closed()
        except Exception:
            pass


async def serve(socket_path: Path = DEFAULT_SOCKET, idle_timeout: float = DEFAULT_IDLE_TIMEOUT):
    global _manager, _last_request, _stop_event

    _manager = SessionManager()
    _last_request = time.monotonic()
    _stop_event = asyncio.Event()

    socket_path.parent.mkdir(parents=True, exist_ok=True)
    if socket_path.exists():
        socket_path.unlink()

    pid_path = socket_path.parent / "daemon.pid"

    server = await asyncio.start_unix_server(handle_client, path=str(socket_path))
    pid_path.write_text(str(os.getpid()))
    print(f"julia-daemon listening on {socket_path}", file=sys.stderr)

    async def watchdog():
        while True:
            await asyncio.sleep(10)
            if _stop_event.is_set() or time.monotonic() - _last_request > idle_timeout:
                server.close()
                return

    async with server:
        watchdog_task = asyncio.create_task(watchdog())
        try:
            await server.serve_forever()
        finally:
            watchdog_task.cancel()
            await _manager.shutdown()
            for p in (socket_path, pid_path):
                try:
                    p.unlink()
                except FileNotFoundError:
                    pass


def main():
    import argparse
    parser = argparse.ArgumentParser(prog="julia-daemon", description="Julia REPL daemon")
    parser.add_argument("--socket", type=Path, default=DEFAULT_SOCKET, metavar="PATH")
    parser.add_argument("--idle-timeout", type=float, default=DEFAULT_IDLE_TIMEOUT, metavar="SECS")
    args = parser.parse_args()
    asyncio.run(serve(args.socket, args.idle_timeout))


if __name__ == "__main__":
    main()
