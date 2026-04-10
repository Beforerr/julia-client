"""julia-client: CLI for interacting with the julia-daemon persistent REPL."""

import argparse
import asyncio
import json
import shutil
import subprocess
import sys
from pathlib import Path

from .daemon import DEFAULT_SOCKET


def _detect_env(start: Path = None) -> str | None:
    """Walk up from start (default: cwd) to find the nearest Project.toml."""
    current = (start or Path.cwd()).resolve()
    for directory in [current, *current.parents]:
        if (directory / "Project.toml").exists():
            return str(directory)
    return None


async def _connect(socket_path: Path, start_if_needed: bool = True):
    """Connect to the daemon socket, starting the daemon if needed."""
    for attempt in range(15):
        try:
            return await asyncio.open_unix_connection(str(socket_path))
        except (FileNotFoundError, ConnectionRefusedError, OSError):
            if not start_if_needed:
                raise RuntimeError(f"julia-daemon is not running (no socket at {socket_path})")
            if attempt == 0:
                _start_daemon(socket_path)
            await asyncio.sleep(0.6)
    raise RuntimeError(f"Could not connect to julia-daemon at {socket_path} after startup — try running 'julia-daemon' manually to see errors")


def _start_daemon(socket_path: Path):
    # Prefer the installed entry point; fall back to module execution
    daemon_exe = shutil.which("julia-daemon")
    if daemon_exe:
        cmd = [daemon_exe, "--socket", str(socket_path)]
    else:
        cmd = [sys.executable, "-m", "julia_client.daemon", "--socket", str(socket_path)]
    subprocess.Popen(cmd, start_new_session=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


async def _request(socket_path: Path, payload: dict, start_if_needed: bool = True) -> dict:
    reader, writer = await _connect(socket_path, start_if_needed)
    try:
        writer.write(json.dumps(payload).encode() + b"\n")
        await writer.drain()
        raw = await reader.readline()
        return json.loads(raw)
    finally:
        writer.close()
        try:
            await writer.wait_closed()
        except Exception:
            pass


async def cmd_eval(args):
    code = sys.stdin.read() if args.code == "-" else args.code
    payload: dict = {"action": "eval", "code": code}
    env = args.env or _detect_env()
    if env:
        payload["env_path"] = str(Path(env).resolve())
    if args.timeout is not None:
        payload["timeout"] = args.timeout
    if args.julia_cmd:
        payload["julia_cmd"] = args.julia_cmd
    result = await _request(args.socket, payload)
    if result["error"]:
        print(result["error"], file=sys.stderr)
        sys.exit(1)
    print(result["output"])


async def cmd_sessions(args):
    try:
        result = await _request(args.socket, {"action": "sessions"}, start_if_needed=False)
    except RuntimeError as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)
    if result["error"]:
        print(result["error"], file=sys.stderr)
        sys.exit(1)
    print(result["output"])


async def cmd_restart(args):
    payload: dict = {"action": "restart"}
    env = args.env or _detect_env()
    if env:
        payload["env_path"] = str(Path(env).resolve())
    try:
        result = await _request(args.socket, payload, start_if_needed=False)
    except RuntimeError as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)
    if result["error"]:
        print(result["error"], file=sys.stderr)
        sys.exit(1)
    print(result["output"])


async def cmd_stop(args):
    try:
        result = await _request(args.socket, {"action": "stop"}, start_if_needed=False)
    except RuntimeError as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)
    if result["error"]:
        print(result["error"], file=sys.stderr)
        sys.exit(1)
    print(result["output"])


def main():
    parser = argparse.ArgumentParser(
        prog="julia-client",
        description="Julia REPL client (daemon: julia-daemon)",
    )
    parser.add_argument(
        "--socket", type=Path, default=DEFAULT_SOCKET, metavar="PATH",
        help=f"Unix socket path (default: {DEFAULT_SOCKET})",
    )

    sub = parser.add_subparsers(dest="command", required=True)

    # eval
    p_eval = sub.add_parser("eval", help="Evaluate Julia code in a persistent session")
    p_eval.add_argument(
        "code", nargs="?", default="-",
        help="Julia code to evaluate (omit or pass - to read from stdin)",
    )
    p_eval.add_argument("--env", metavar="PATH", help="Julia project directory")
    p_eval.add_argument(
        "--timeout", type=float, metavar="SECS",
        help="Timeout in seconds (0 = no timeout, default: 60; auto-disabled for Pkg operations)",
    )
    p_eval.add_argument("--julia-cmd", metavar="CMD", help='Custom Julia binary, e.g. "julia +1.11"')

    # sessions
    sub.add_parser("sessions", help="List active Julia sessions")

    # restart
    p_restart = sub.add_parser("restart", help="Restart a Julia session, clearing all state")
    p_restart.add_argument("--env", metavar="PATH", help="Project directory (default: temp session)")

    # stop
    sub.add_parser("stop", help="Stop the daemon")

    args = parser.parse_args()

    dispatch = {
        "eval": cmd_eval,
        "sessions": cmd_sessions,
        "restart": cmd_restart,
        "stop": cmd_stop,
    }
    asyncio.run(dispatch[args.command](args))


if __name__ == "__main__":
    main()
