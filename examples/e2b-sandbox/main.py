"""Pause and resume a durable kfuse session across E2B sandboxes.

Runs a scripted pause/resume demo by default; ``--repl`` opens an interactive
prompt on the same durable session instead.
"""

from __future__ import annotations

import argparse
import shlex
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from threading import Lock

from e2b import Sandbox, SandboxException
from rich.console import Console
from rich.panel import Panel
from rich.table import Table
from rich.text import Text
from rich.tree import Tree

from kfuse_e2b import (
    CommandFailedError,
    blob_prefix,
    branch_session,
    build_kfuse_binary,
    checkpoint,
    close_sandbox,
    create_sandbox,
    load_env,
    mount,
    run,
    sandbox_env,
)

ORIGIN_LAST_LINE = ">>> last line of the original file <<<"
BRANCHING_READ_PATH_NOTE = (
    "Each new sandbox starts with an empty disk. "
    "`kfuse session branch <parent> --to <offset>` builds the child's state "
    "from the parent's checkpoint image in S3 plus a replay of the parent's "
    "Kafka events up to that offset. The state is only metadata: paths → byte "
    "ranges → S3 blob ids. Nothing is copied — the child shares the origin's "
    "blobs, and `cat`/`sha256sum` fetch those blobs from S3 on demand through "
    "the FUSE mount. Writes in a branch create new blobs and Kafka events for "
    "that branch only, so branches never see each other's changes."
)


class PersistentSandbox:
    """A kfuse session that outlives any single E2B sandbox."""

    def __init__(
        self,
        env: dict[str, str],
        binary: Path,
        api_key: str,
        console: Console,
        timeout: int = 600,
    ):
        self.env = env
        self.binary = binary
        self.api_key = api_key
        self.console = console
        self.timeout = timeout
        self.sandbox: Sandbox | None = None
        self.session_id: str | None = None

    def _new_sandbox(self, session_id: str | None = None) -> None:
        self.console.print("[dim]provisioning sandbox...[/]")
        sandbox = create_sandbox(
            self.env, timeout=self.timeout, binary=self.binary, api_key=self.api_key
        )
        self.sandbox = sandbox
        try:
            mounted_session = mount(sandbox, session_id)
        except Exception:
            self.sandbox = None
            try:
                close_sandbox(sandbox)
            except SandboxException:
                pass
            raise
        self.session_id = mounted_session
        verb = "resumed" if session_id is not None else "mounted"
        self.console.print(f"[green]sandbox {verb}[/] session {self.session_id}")

    def close(self) -> None:
        if self.sandbox is not None:
            try:
                self.console.print("[dim]unmounting and killing sandbox...[/]")
                close_sandbox(self.sandbox)
            finally:
                self.sandbox = None

    def restart(self) -> None:
        session_id = self.session_id
        self.close()
        self._new_sandbox(session_id)

    def checkpoint(self) -> str:
        if self.sandbox is None:
            self._new_sandbox()
        offset = checkpoint(self.sandbox)
        self.console.print(f"[green]checkpoint[/] offset {offset}")
        return offset

    def execute(self, command: str) -> str:
        if self.sandbox is None:
            self._new_sandbox()
        try:
            return run(self.sandbox, command).stdout
        except CommandFailedError:
            raise
        except SandboxException as exc:
            self.console.print(
                f"[yellow]sandbox unavailable; recreating it ({exc})[/]"
            )
            session_id = self.session_id
            self.close()
            self._new_sandbox(session_id)
            return run(self.sandbox, command).stdout


def demo(
    console: Console,
    env: dict[str, str],
    binary: Path,
    api_key: str,
) -> None:
    sandboxes: list[Sandbox] = []
    sandbox_lock = Lock()
    parent_id: str | None = None
    base_offset: str | None = None
    base_lines = 0
    base_sha = ""
    base_last_line = ""
    branch_results: dict[str, dict[str, object]] = {}

    try:
        console.rule("[bold]Step 1/8 — Provision a sandbox[/]")
        console.print(
            "[origin] creating an E2B sandbox and mounting the workspace",
            markup=False,
        )
        origin = create_sandbox(
            env, timeout=600, binary=binary, api_key=api_key
        )
        sandboxes.append(origin)
        parent_id = mount(origin)
        console.print(f"[origin] session {parent_id}", markup=False)

        console.rule("[bold]Step 2/8 — Write 10 MB of activity into activity.txt[/]")
        console.print(
            "[origin] generating activity.txt and copying it into the kfuse mount",
            markup=False,
        )
        run(
            origin,
            "awk 'BEGIN{for(i=1;i<=219999;i++) printf "
            "\"%07d agent-step tool=shell status=ok elapsed=%03dms\\n\", "
            "i, i%1000}' > /tmp/activity.txt && "
            f"printf '%s\\n' {shlex.quote(ORIGIN_LAST_LINE)} "
            ">> /tmp/activity.txt && "
            "cp /tmp/activity.txt activity.txt && rm /tmp/activity.txt",
        )
        size = int(run(origin, "stat -c %s activity.txt").stdout.strip())
        lines = int(run(origin, "wc -l < activity.txt").stdout.strip())
        sha = run(
            origin, "sha256sum activity.txt | cut -d\" \" -f1"
        ).stdout.strip()
        last_line = run(origin, "tail -n 1 activity.txt").stdout.strip()
        console.print(
            f"[origin] activity.txt is {size / 1e6:.1f} MB, "
            f"{lines} lines, sha {sha[:12]}..., last line: {last_line}",
            markup=False,
        )
        if size < 10_000_000:
            raise RuntimeError(f"activity.txt is too small: {size} bytes")
        base_lines = lines
        base_sha = sha
        base_last_line = last_line
        if base_last_line != ORIGIN_LAST_LINE:
            raise RuntimeError(
                f"unexpected origin last line: {base_last_line!r}"
            )
        base_offset = checkpoint(origin)
        console.print(
            f"[origin] checkpoint offset {base_offset} "
            "(state + blobs flushed to S3)",
            markup=False,
        )

        console.rule("[bold]Step 3/8 — Kill the origin sandbox[/]")
        console.print(
            "[origin] closing the sandbox; the only copy now lives in Kafka+S3",
            markup=False,
        )
        close_sandbox(origin)
        sandboxes.remove(origin)

        console.rule(
            "[bold]Step 4/8 — Branch: two new sandboxes on top of the checkpoint[/]"
        )
        branch_specs = (
            ("branch-kafka", "Kafka ❤️ Agents"),
            ("branch-s3", "S3 ❤️ Agents"),
        )

        def run_branch(label: str, line: str) -> dict[str, object]:
            sandbox = create_sandbox(
                env, timeout=600, binary=binary, api_key=api_key
            )
            with sandbox_lock:
                sandboxes.append(sandbox)
            if parent_id is None or base_offset is None:
                raise RuntimeError("origin session was not initialized")
            child_id = branch_session(sandbox, parent_id, base_offset)
            mount(sandbox, child_id)
            branch_sha = run(
                sandbox, "sha256sum activity.txt | cut -d\" \" -f1"
            ).stdout.strip()
            branch_lines = int(run(sandbox, "wc -l < activity.txt").stdout.strip())
            if branch_sha != base_sha or branch_lines != base_lines:
                raise RuntimeError(
                    f"{label} did not reproduce activity.txt: "
                    f"{branch_lines} lines, sha {branch_sha}"
                )
            branch_size = int(
                run(sandbox, "stat -c %s activity.txt").stdout.strip()
            )
            run(
                sandbox,
                f"printf '%s\\n' {shlex.quote(line)} >> activity.txt",
            )
            tail = run(sandbox, "tail -n 1 activity.txt").stdout.strip()
            if tail != line:
                raise RuntimeError(
                    f"{label} append was not visible: {tail!r}"
                )
            branch_offset = checkpoint(sandbox)
            branch_total_lines = int(
                run(sandbox, "wc -l < activity.txt").stdout.strip()
            )
            return {
                "label": label,
                "sandbox": sandbox,
                "child_id": child_id,
                "line": line,
                "tail": tail,
                "offset": branch_offset,
                "size": branch_size,
                "lines": branch_total_lines,
            }

        with ThreadPoolExecutor(max_workers=2) as executor:
            futures = {
                label: executor.submit(run_branch, label, line)
                for label, line in branch_specs
            }
            for label, _ in branch_specs:
                try:
                    branch_results[label] = futures[label].result()
                except Exception as exc:
                    raise RuntimeError(f"{label} failed: {exc}") from exc

        for label, _ in branch_specs:
            result = branch_results[label]
            console.print(
                f"[{label}] session {result['child_id']} sees activity.txt "
                f"{result['size'] / 1e6:.1f} MB, "
                f"sha {base_sha[:12]}... (identical to origin)",
                markup=False,
            )

        console.print(
            Panel(
                Text(BRANCHING_READ_PATH_NOTE),
                title="How the new sandboxes see activity.txt",
                border_style="cyan",
            )
        )

        for step, label in ((5, "branch-kafka"), (6, "branch-s3")):
            console.rule(
                f"[bold]Step {step}/8 — Append a line in {label}[/]"
            )
            result = branch_results[label]
            console.print(
                f"[{label}] appended {result['line']!r}; "
                f"tail verified as {result['tail']!r}; "
                f"checkpoint offset {result['offset']}",
                markup=False,
            )

        console.rule("[bold]Step 7/8 — Kill both branch sandboxes[/]")
        for label, _ in branch_specs:
            result = branch_results[label]
            sandbox = result["sandbox"]
            if not isinstance(sandbox, Sandbox):
                raise RuntimeError(f"{label} sandbox handle is missing")
            console.print(f"[{label}] closing sandbox", markup=False)
            close_sandbox(sandbox)
            sandboxes.remove(sandbox)

        console.rule(
            "[bold]Step 8/8 — Replay: one sandbox from the checkpoint, "
            "one from latest[/]"
        )
        if parent_id is None or base_offset is None:
            raise RuntimeError("origin session was not initialized")
        summary_rows: list[tuple[str, str, str, int, str]] = []
        summary_rows.append(
            ("origin", parent_id, "checkpoint", base_lines, base_last_line)
        )
        for label, source in (
            ("branch-kafka", "origin checkpoint + append"),
            ("branch-s3", "origin checkpoint + append"),
        ):
            result = branch_results[label]
            summary_rows.append(
                (
                    label,
                    str(result["child_id"]),
                    source,
                    int(result["lines"]),
                    str(result["tail"]),
                )
            )

        replay_checkpoint = create_sandbox(
            env, timeout=600, binary=binary, api_key=api_key
        )
        sandboxes.append(replay_checkpoint)
        checkpoint_child = branch_session(
            replay_checkpoint, parent_id, base_offset
        )
        mount(replay_checkpoint, checkpoint_child)
        checkpoint_tail = run(
            replay_checkpoint, "tail -n 1 activity.txt"
        ).stdout.strip()
        checkpoint_lines = int(
            run(replay_checkpoint, "wc -l < activity.txt").stdout.strip()
        )
        checkpoint_sha = run(
            replay_checkpoint, "sha256sum activity.txt | cut -d\" \" -f1"
        ).stdout.strip()
        if (
            checkpoint_tail != base_last_line
            or checkpoint_lines != base_lines
            or checkpoint_sha != base_sha
        ):
            raise RuntimeError("replay@checkpoint did not match the checkpoint")
        console.print(
            f"[replay@checkpoint] {checkpoint_lines} lines, last line: "
            f"{checkpoint_tail!r} — appended activity NOT present (as expected)",
            markup=False,
        )
        summary_rows.append(
            (
                "replay@checkpoint",
                checkpoint_child,
                "origin checkpoint",
                checkpoint_lines,
                checkpoint_tail,
            )
        )

        kafka_child = str(branch_results["branch-kafka"]["child_id"])
        replay_latest = create_sandbox(
            env, timeout=600, binary=binary, api_key=api_key
        )
        sandboxes.append(replay_latest)
        latest_child = branch_session(replay_latest, kafka_child, None)
        mount(replay_latest, latest_child)
        latest_tail = run(replay_latest, "tail -n 1 activity.txt").stdout.strip()
        latest_lines = int(run(replay_latest, "wc -l < activity.txt").stdout.strip())
        if latest_tail != "Kafka ❤️ Agents" or latest_lines != base_lines + 1:
            raise RuntimeError("replay@latest did not include the branch append")
        console.print(
            f"[replay@latest] {latest_lines} lines, last line: "
            "'Kafka ❤️ Agents' — appended activity present",
            markup=False,
        )
        console.print(
            Panel(
                Text(
                    f"replay@checkpoint branched from origin at offset "
                    f"{base_offset}: the image + events up to that offset "
                    "contain no appended line. replay@latest branched from "
                    "branch-kafka's committed tail, so it includes the "
                    "'Kafka ❤️ Agents' append. Both were created from S3 + "
                    "Kafka alone; every sandbox that wrote the data was "
                    "already dead."
                ),
                title="Two points in history",
                border_style="cyan",
            )
        )
        summary_rows.append(
            (
                "replay@latest",
                latest_child,
                "branch-kafka latest",
                latest_lines,
                latest_tail,
            )
        )

        console.rule("[bold]Session lineage[/]")
        tree = Tree(
            f"[bold]origin[/]  [dim]{parent_id}[/]  activity.txt "
            f"{base_lines} lines · checkpoint @{base_offset}"
        )
        kafka_branch = tree.add(
            f"[bold]branch-kafka[/]  "
            f"[dim]{branch_results['branch-kafka']['child_id']}[/]  "
            f"branched @{base_offset} · + \"Kafka ❤️ Agents\" → "
            f"{branch_results['branch-kafka']['lines']} lines · checkpoint @"
            f"{branch_results['branch-kafka']['offset']}"
        )
        kafka_branch.add(
            f"[bold]replay@latest[/]  [dim]{latest_child}[/]  "
            "branched from branch-kafka tail → "
            f"{latest_lines} lines · last: \"Kafka ❤️ Agents\""
        )
        tree.add(
            f"[bold]branch-s3[/]  "
            f"[dim]{branch_results['branch-s3']['child_id']}[/]  "
            f"branched @{base_offset} · + \"S3 ❤️ Agents\" → "
            f"{branch_results['branch-s3']['lines']} lines · checkpoint @"
            f"{branch_results['branch-s3']['offset']}"
        )
        tree.add(
            f"[bold]replay@checkpoint[/]  [dim]{checkpoint_child}[/]  "
            f"branched @{base_offset} → {checkpoint_lines} lines · last: "
            f"\"{ORIGIN_LAST_LINE}\""
        )
        console.print(tree)
        console.rule("[bold]Summary[/]")
        table = Table()
        table.add_column("Sandbox", no_wrap=True)
        table.add_column("Session", no_wrap=True)
        table.add_column("Source")
        table.add_column("Lines", justify="right", no_wrap=True)
        table.add_column("Last line")
        for label, session, source, count, last in summary_rows:
            table.add_row(label, session[:12], source, str(count), last)
        console.print(table)
        console.print("[bold green]COMPLETE[/]")
    finally:
        for sandbox in list(sandboxes):
            try:
                close_sandbox(sandbox)
            except SandboxException as exc:
                print(f"warning: failed to close sandbox: {exc}")


def repl(controller: PersistentSandbox) -> None:
    console = controller.console
    console.print(
        "[bold]Type commands at the prompt.[/] "
        "Hint: [cyan]kfuse status[/], [cyan]kfuse session ls[/], and "
        "[cyan]kfuse checkpoint[/] are available; [cyan]:restart[/] moves the "
        "session to a new sandbox."
    )
    while True:
        try:
            command = input("e2b> ")
        except EOFError:
            break
        if command.strip() == "exit":
            break
        if command.strip() == ":restart":
            controller.restart()
            continue
        if not command.strip():
            continue
        try:
            output = controller.execute(command)
            if output:
                print(output, end="" if output.endswith("\n") else "\n")
        except CommandFailedError as exc:
            console.print(f"[red]command failed:[/] {exc}")
        except (
            SandboxException,
            OSError,
            RuntimeError,
            subprocess.SubprocessError,
        ) as exc:
            console.print(f"[red]command failed:[/] {exc}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--repl",
        action="store_true",
        help="open an interactive prompt instead of running the scripted demo",
    )
    args = parser.parse_args()

    creds = load_env()
    binary = build_kfuse_binary()
    env = sandbox_env(creds, blob_prefix())
    console = Console()
    if args.repl:
        controller = PersistentSandbox(
            env,
            binary,
            creds["E2B_API_KEY"],
            console,
        )
        try:
            repl(controller)
        finally:
            controller.close()
    else:
        demo(console, env, binary, creds["E2B_API_KEY"])


if __name__ == "__main__":
    try:
        main()
    except (
        SandboxException,
        CommandFailedError,
        OSError,
        RuntimeError,
        subprocess.SubprocessError,
    ) as exc:
        raise SystemExit(f"E2B DEMO FAILED: {exc}") from exc
