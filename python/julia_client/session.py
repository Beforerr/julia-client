import asyncio
import atexit
import os
import re
import shlex
import shutil
import subprocess
import tempfile
import time
import uuid
from io import TextIOWrapper
from pathlib import Path

DEFAULT_TIMEOUT = 60.0
DEFAULT_JULIA_ARGS = ("--threads=auto",)
PKG_PATTERN = re.compile(r"\bPkg\.")
TEMP_SESSION_KEY = "__temp__"


class JuliaSession:
    def __init__(
        self,
        env_dir,
        sentinel,
        *,
        is_temp=False,
        is_test=False,
        julia_args=DEFAULT_JULIA_ARGS,
        julia_cmd=None,
        log_file=None,
    ):
        self.env_dir = env_dir
        self.sentinel = sentinel
        self.is_temp = is_temp
        self.is_test = is_test
        self.julia_args = julia_args
        self.julia_cmd = julia_cmd
        self.process: asyncio.subprocess.Process | None = None
        self.lock = asyncio.Lock()
        self._log_file: TextIOWrapper | None = log_file

    @property
    def project_path(self):
        if self.is_test:
            return str(Path(self.env_dir).parent)
        return self.env_dir

    @property
    def init_code(self):
        if self.is_test:
            return "using TestEnv; TestEnv.activate()"
        return None

    async def start(self):
        parts = shlex.split(self.julia_cmd) if self.julia_cmd else ["julia"]
        executable = parts[0]
        remaining = parts[1:]
        if remaining and remaining[0].startswith("+"):
            channel_args = [remaining[0]]
            extra_flags = remaining[1:]
        else:
            channel_args = []
            extra_flags = remaining

        if not os.path.isabs(executable):
            resolved = shutil.which(executable)
            if resolved is None:
                raise RuntimeError(
                    f"'{executable}' not found in PATH. Install Julia from https://julialang.org/downloads/"
                )
            executable = resolved

        cmd = [
            executable,
            *channel_args,
            "-i",
            *self.julia_args,
            *extra_flags,
            f"--project={self.project_path}",
        ]

        self.process = await asyncio.create_subprocess_exec(
            *cmd,
            cwd=self.env_dir,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            limit=64 * 1024 * 1024,
        )

        await self._execute_raw("", timeout=120.0)
        await self._execute_raw("try; using Revise; catch; end", timeout=120.0)
        if self.init_code:
            await self._execute_raw(self.init_code, timeout=None)

    def is_alive(self):
        return self.process is not None and self.process.returncode is None

    async def execute(self, code, timeout):
        async with self.lock:
            if not self.is_alive():
                raise RuntimeError("Julia session has died unexpectedly")
            hex_encoded = code.encode().hex()
            wrapped = (
                f'try; Revise.revise(); catch; end;'
                f'include_string(Main, String(hex2bytes("{hex_encoded}")));'
                f'nothing'
            )
            if self._log_file:
                ts = time.strftime("%H:%M:%S")
                self._log_file.write(f"[{ts}] julia> {code}\n")
                self._log_file.flush()
            output = await self._execute_raw(wrapped, timeout)
            if self._log_file and output:
                self._log_file.write(f"{output}\n\n")
                self._log_file.flush()
            return output

    async def _execute_raw(self, code, timeout):
        assert self.process is not None
        assert self.process.stdin is not None

        sentinel_cmd = (
            f'flush(stderr); write(stdout, "\\n"); println(stdout, "{self.sentinel}"); flush(stdout)'
        )
        payload = code + "\n" + sentinel_cmd + "\n"
        self.process.stdin.write(payload.encode())
        await self.process.stdin.drain()

        lines: list[str] = []

        async def read_until_sentinel():
            while True:
                raw = await self.process.stdout.readline()
                if not raw:
                    collected = "\n".join(lines)
                    raise RuntimeError(
                        f"Julia process died during execution.\nOutput before death:\n{collected}"
                    )
                line = raw.decode().rstrip("\n").rstrip("\r")
                if line == self.sentinel:
                    break
                lines.append(line)
            if lines and lines[-1] == "":
                lines.pop()
            return "\n".join(lines)

        if timeout is not None:
            try:
                return await asyncio.wait_for(read_until_sentinel(), timeout=timeout)
            except asyncio.TimeoutError:
                self.process.kill()
                await self.process.wait()
                partial = "\n".join(lines)
                msg = f"Execution timed out after {timeout}s. Session killed; it will restart on next call."
                if partial:
                    msg += f"\n\nOutput before timeout:\n{partial}"
                raise RuntimeError(msg)
        else:
            return await read_until_sentinel()

    async def kill(self):
        if self.process is not None and self.process.returncode is None:
            self.process.kill()
            await self.process.wait()
        if self.is_temp and os.path.isdir(self.env_dir):
            shutil.rmtree(self.env_dir, ignore_errors=True)


class SessionManager:
    def __init__(self, julia_args=DEFAULT_JULIA_ARGS):
        self.julia_args = julia_args
        self._sessions: dict[str, JuliaSession] = {}
        self._create_locks: dict[str, asyncio.Lock] = {}
        self._global_lock = asyncio.Lock()
        self._log_dir = tempfile.mkdtemp(prefix="julia-client-logs-")
        self._log_files: dict[str, TextIOWrapper] = {}
        atexit.register(self._cleanup_logs)

    def _get_log_file(self, key):
        if key not in self._log_files:
            safe_name = key.replace("/", "_").replace("\\", "_").strip("_") or "temp"
            path = os.path.join(self._log_dir, f"{safe_name}.log")
            self._log_files[key] = open(path, "a")
        return self._log_files[key]

    def _cleanup_logs(self):
        for f in self._log_files.values():
            try:
                f.close()
            except Exception:
                pass
        shutil.rmtree(self._log_dir, ignore_errors=True)

    def _key(self, env_path):
        if env_path is None:
            return TEMP_SESSION_KEY
        return str(Path(env_path).resolve())

    async def get_or_create(self, env_path, julia_cmd=None):
        key = self._key(env_path)

        if key in self._sessions and self._sessions[key].is_alive():
            if self._sessions[key].julia_cmd == julia_cmd:
                return self._sessions[key]
            await self._sessions[key].kill()
            del self._sessions[key]

        async with self._global_lock:
            if key not in self._create_locks:
                self._create_locks[key] = asyncio.Lock()
            create_lock = self._create_locks[key]

        async with create_lock:
            if key in self._sessions and self._sessions[key].is_alive():
                if self._sessions[key].julia_cmd == julia_cmd:
                    return self._sessions[key]
                await self._sessions[key].kill()
                del self._sessions[key]

            if key in self._sessions:
                await self._sessions[key].kill()
                del self._sessions[key]

            sentinel = f"__JULIA_CLIENT_{uuid.uuid4().hex}__"
            is_temp = env_path is None
            if is_temp:
                env_dir = tempfile.mkdtemp(prefix="julia-client-")
                is_test = False
            else:
                resolved = Path(env_path).resolve()
                env_dir = str(resolved)
                is_test = resolved.name == "test"

            session = JuliaSession(
                env_dir, sentinel,
                is_temp=is_temp, is_test=is_test,
                julia_args=self.julia_args,
                julia_cmd=julia_cmd,
                log_file=self._get_log_file(key),
            )
            await session.start()
            self._sessions[key] = session
            return session

    async def restart(self, env_path):
        key = self._key(env_path)
        if key in self._sessions:
            await self._sessions[key].kill()
            del self._sessions[key]

    def list_sessions(self):
        result = []
        for key, session in self._sessions.items():
            info = {
                "env_path": session.env_dir,
                "alive": session.is_alive(),
                "temp": session.is_temp,
            }
            if session.julia_cmd is not None:
                info["julia_cmd"] = session.julia_cmd
            if key in self._log_files:
                info["log_file"] = self._log_files[key].name
            result.append(info)
        return result

    async def shutdown(self):
        for session in self._sessions.values():
            await session.kill()
        self._sessions.clear()
        self._cleanup_logs()
