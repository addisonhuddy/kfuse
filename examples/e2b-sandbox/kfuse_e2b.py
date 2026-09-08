"""Shared helpers for the E2B kfuse examples."""

from __future__ import annotations

import os
import re
import secrets
import shlex
import subprocess
import time
from pathlib import Path

from e2b import CommandExitException, CommandResult, Sandbox, SandboxException

MOUNTPOINT = "/home/user/work"
LOWER_ID = "e2b-demo"
EXEC_TIMEOUT = 300
LEASE_TTL = 45
LEASE_RETRY_DELAY = 47
REMOTE_BINARY = "/home/user/kfuse"

_STATUS_SESSION_RE = re.compile(r"mounted: session ([A-Za-z0-9._-]+) pid \d+")
_LEASE_FAILURE_RE = re.compile(r"(?:no live mount|lease|session locked)", re.IGNORECASE)
SESSION_ID_RE = re.compile(r"^[A-Za-z0-9._-]+$", re.MULTILINE)

_CANONICAL_ALIASES = {
    "KF_KAFKA_BROKERS": "BOOTSTRAP_SERVER",
    "KF_KAFKA_SASL_USERNAME": "CONFLUENT_CLOUD_KEY",
    "KF_KAFKA_SASL_PASSWORD": "CONFLUENT_CLOUD_SECRET",
    "AWS_REGION": "REGION",
    "AWS_ACCESS_KEY_ID": "AWS_ACCESS_KEY",
    "AWS_SECRET_ACCESS_KEY": "AWS_SECRET_KEY",
    "KF_BLOB_BUCKET": "BUCKET",
}


def _read_env_file(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    if not path.is_file():
        return values
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key):
            continue
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
            value = value[1:-1]
        values[key] = value
    return values


def load_env(creds: Path | None = None) -> dict[str, str]:
    """Load credentials, accepting the names used by the other examples."""

    repo_root = Path(__file__).resolve().parents[2]
    env_file = repo_root / ".env" if creds is None else creds
    values = _read_env_file(env_file)
    values.update({key: value for key, value in os.environ.items() if value})

    resolved: dict[str, str] = {}
    for canonical, alias in _CANONICAL_ALIASES.items():
        for key in (canonical, alias):
            if values.get(key, ""):
                resolved[canonical] = values[key]
                break
    if values.get("E2B_API_KEY", ""):
        resolved["E2B_API_KEY"] = values["E2B_API_KEY"]
    if values.get("KF_KAFKA_TOPIC", ""):
        resolved["KF_KAFKA_TOPIC"] = values["KF_KAFKA_TOPIC"]

    required = [*_CANONICAL_ALIASES, "E2B_API_KEY"]
    missing = [key for key in required if not resolved.get(key)]
    if missing:
        raise SystemExit(
            "missing required environment variables: " + ", ".join(missing)
        )
    return resolved


def sandbox_env(creds_values: dict[str, str], blob_prefix: str) -> dict[str, str]:
    """Return only the kfuse settings that are safe to pass into a sandbox."""

    env = {key: creds_values[key] for key in _CANONICAL_ALIASES}
    env.update(
        {
            "KF_KAFKA_TLS": "true",
            "KF_KAFKA_TOPIC": creds_values.get("KF_KAFKA_TOPIC", "kfuse.events"),
            "KF_KAFKA_PARTITIONS": "8",
            "KF_BLOB_PREFIX": blob_prefix,
            "KF_LOWER_ID": LOWER_ID,
            "KF_STATE_DIR": "/home/user/.kfuse",
        }
    )
    return env


def blob_prefix() -> str:
    return f"kfuse/demo/e2b-sandbox/{int(time.time())}-{secrets.token_hex(2)}/"


def build_kfuse_binary() -> Path:
    repo_root = Path(__file__).resolve().parents[2]
    output = Path(__file__).resolve().parent / ".build" / "kfuse"
    output.parent.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    env.update({"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"})
    subprocess.run(
        ["go", "build", "-o", str(output), "./cmd/kfuse"],
        cwd=repo_root,
        env=env,
        check=True,
    )
    return output


class CommandFailedError(RuntimeError):
    """A command ran but returned a non-zero exit code."""


def _run_command(
    sandbox: Sandbox, cmd: str, *, user: str, cwd: str
) -> CommandResult:
    try:
        response = sandbox.commands.run(
            cmd,
            user=user,
            cwd=cwd,
            timeout=EXEC_TIMEOUT,
        )
    except CommandExitException as exc:
        raise CommandFailedError(
            f"command failed with exit code {exc.exit_code}: {cmd}\n"
            f"stdout:\n{exc.stdout}\n"
            f"stderr:\n{exc.stderr}"
        ) from exc
    if response.exit_code != 0:
        raise CommandFailedError(
            f"command failed with exit code {response.exit_code}: {cmd}\n"
            f"stdout:\n{response.stdout}\n"
            f"stderr:\n{response.stderr}"
        )
    return response


def provision(sandbox: Sandbox, binary: Path) -> None:
    """Install kfuse and prepare the lower directory before mounting."""

    sandbox.files.write(REMOTE_BINARY, binary.read_bytes(), user="user")
    _run_command(
        sandbox,
        (
            f"install -m 755 {REMOTE_BINARY} /usr/local/bin/kfuse && "
            f"mkdir -p {MOUNTPOINT} && "
            f"printf 'base file, served read-only from the image\\n' "
            f"> {MOUNTPOINT}/base.txt && "
            f"chown -R user:user {MOUNTPOINT}"
        ),
        user="root",
        cwd="/home/user",
    )


def create_sandbox(
    env_vars: dict[str, str], timeout: int, binary: Path, api_key: str
) -> Sandbox:
    sandbox = Sandbox.create(timeout=timeout, envs=env_vars, api_key=api_key)
    try:
        provision(sandbox, binary)
    except (SandboxException, OSError, RuntimeError):
        sandbox.kill()
        raise
    return sandbox


def run(sandbox: Sandbox, cmd: str) -> CommandResult:
    return _run_command(sandbox, cmd, user="user", cwd=MOUNTPOINT)


def run_kfuse(sandbox: Sandbox, cmd: str) -> CommandResult:
    """Run a kfuse CLI command from outside the mountpoint.

    A cwd inside the mountpoint pins the FUSE mount, so fusermount reports
    EBUSY and the daemon keeps holding the session lease. Commands must
    therefore name the lower directory explicitly via `--lower`.
    """
    return _run_command(sandbox, cmd, user="user", cwd="/home/user")


def close_sandbox(sandbox: Sandbox) -> None:
    try:
        run_kfuse(sandbox, f"kfuse umount --lower {MOUNTPOINT}")
    except (CommandFailedError, SandboxException) as exc:
        print(
            f"warning: kfuse umount failed ({exc}); "
            f"the lease may take {LEASE_TTL}s to expire"
        )
    sandbox.kill()


def _is_lease_failure(error: BaseException) -> bool:
    return _LEASE_FAILURE_RE.search(str(error)) is not None


def mount(sandbox: Sandbox, session_id: str | None = None) -> str:
    """Mount the workspace and return the session id reported by kfuse status."""

    command = "kfuse mount"
    attempts = 1
    if session_id is not None:
        if SESSION_ID_RE.fullmatch(session_id) is None:
            raise ValueError(f"invalid kfuse session id: {session_id!r}")
        command += f" {session_id}"
        attempts = 2
    command += f" --lower {MOUNTPOINT}"
    for attempt in range(attempts):
        try:
            run_kfuse(sandbox, command)
            status = run_kfuse(sandbox, f"kfuse status --lower {MOUNTPOINT}")
            run_kfuse(sandbox, f"grep -q ' {MOUNTPOINT} ' /proc/mounts")
        except (CommandFailedError, SandboxException) as exc:
            if (
                session_id is None
                or attempt + 1 == attempts
                or not _is_lease_failure(exc)
            ):
                raise
            print(
                "previous sandbox lease is still active; "
                f"waiting {LEASE_RETRY_DELAY}s before retrying"
            )
            time.sleep(LEASE_RETRY_DELAY)
            continue
        match = _STATUS_SESSION_RE.search(status.stdout)
        if match is None:
            raise RuntimeError(
                f"kfuse status reported no mount; stdout:\n{status.stdout}\n"
                f"stderr:\n{status.stderr}"
            )
        mounted = match.group(1)
        if session_id is not None and mounted != session_id:
            raise RuntimeError(
                f"kfuse mounted session {mounted!r}, expected {session_id!r}"
            )
        return mounted
    raise AssertionError("mount retry loop exhausted")


def checkpoint(sandbox: Sandbox) -> str:
    output = run(sandbox, "kfuse checkpoint").stdout
    lines = [line.strip() for line in output.splitlines() if line.strip()]
    if not lines or not re.fullmatch(r"-?\d+", lines[-1]):
        raise RuntimeError(
            f"kfuse checkpoint produced no Kafka offset; stdout was:\n{output}"
        )
    return lines[-1]


def branch_session(
    sandbox: Sandbox, parent_id: str, offset: str | None
) -> str:
    """Create a child session and return its id."""

    command = f"kfuse session branch {shlex.quote(parent_id)}"
    if offset is not None:
        command += f" --to {shlex.quote(offset)}"
    command += f" --lower {MOUNTPOINT}"
    output = run_kfuse(sandbox, command).stdout
    match = SESSION_ID_RE.search(output)
    if match is None:
        raise RuntimeError(
            f"kfuse session branch printed no session id:\n{output}"
        )
    return match.group(0)
