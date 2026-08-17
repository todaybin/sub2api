#!/usr/bin/env python3
"""Read-only preflight checks for the sub2api upstream release workflow."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple


def run_git(
    *arguments: str, cwd: Optional[Path] = None, timeout: int = 30
) -> List[str]:
    try:
        result = subprocess.run(
            ["git", *arguments],
            cwd=cwd,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError(
            f"git {' '.join(arguments)} timed out after {timeout} seconds"
        ) from exc
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip()
        raise RuntimeError(f"git {' '.join(arguments)} failed: {detail}")
    return result.stdout.splitlines()


def test_ref(repository_root: Path, ref: str) -> bool:
    result = subprocess.run(
        ["git", "show-ref", "--verify", "--quiet", ref],
        cwd=repository_root,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def first_line(lines: List[str], description: str) -> str:
    if not lines:
        raise RuntimeError(f"Missing Git output for {description}")
    return lines[0].strip()


def remote_identity(url: str) -> str:
    identity = url.strip().replace("\\", "/")
    identity = re.sub(r"^git@([^:]+):", r"\1/", identity)
    identity = re.sub(r"^[a-zA-Z][a-zA-Z0-9+.-]*://", "", identity)
    return re.sub(r"\.git$", "", identity.rstrip("/")).lower()


def official_branch(repository_root: Path) -> str:
    result = subprocess.run(
        ["git", "symbolic-ref", "--quiet", "--short", "refs/remotes/upstream/HEAD"],
        cwd=repository_root,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if result.returncode == 0 and result.stdout.strip():
        return re.sub(r"^upstream/", "", result.stdout.strip())
    for candidate in ("main", "master"):
        if test_ref(repository_root, f"refs/remotes/upstream/{candidate}"):
            return candidate
    raise RuntimeError("Cannot determine upstream default branch")


def parse_version(repository_root: Path) -> Tuple[str, str]:
    version_file = repository_root / "backend" / "cmd" / "server" / "VERSION"
    if not version_file.is_file():
        raise RuntimeError(f"Missing version file: {version_file}")
    current = version_file.read_text(encoding="utf-8").strip()
    match = re.fullmatch(r"(?P<base>\d+\.\d+\.\d+)(?:-custom\.\d+)?", current)
    if not match:
        raise RuntimeError(f"Unsupported VERSION value: {current}")
    return current, match.group("base")


def next_custom_version(repository_root: Path, base_version: str) -> Tuple[str, str]:
    pattern = re.compile(rf"^v{re.escape(base_version)}-custom\.(\d+)$")
    numbers: List[int] = []
    for tag in run_git("tag", "--list", f"v{base_version}-custom.*", cwd=repository_root):
        match = pattern.fullmatch(tag.strip())
        if match:
            numbers.append(int(match.group(1)))

    remote_lines = run_git(
        "-c",
        "http.lowSpeedLimit=1",
        "-c",
        "http.lowSpeedTime=10",
        "ls-remote",
        "--tags",
        "origin",
        f"refs/tags/v{base_version}-custom.*",
        cwd=repository_root,
        timeout=20,
    )
    remote_pattern = re.compile(
        rf"refs/tags/v{re.escape(base_version)}-custom\.(\d+)(?:\^\{{\}})?$"
    )
    for line in remote_lines:
        match = remote_pattern.search(line)
        if match:
            numbers.append(int(match.group(1)))
    next_number = max(numbers, default=0) + 1
    version = f"{base_version}-custom.{next_number}"
    return version, f"v{version}"


def operation_state(repository_root: Path) -> Dict[str, bool]:
    git_dir_text = first_line(
        run_git("rev-parse", "--git-dir", cwd=repository_root), "Git directory"
    )
    git_dir = Path(git_dir_text)
    if not git_dir.is_absolute():
        git_dir = repository_root / git_dir
    return {
        "merge": (git_dir / "MERGE_HEAD").exists(),
        "rebase": (git_dir / "rebase-merge").exists()
        or (git_dir / "rebase-apply").exists(),
        "cherry_pick": (git_dir / "CHERRY_PICK_HEAD").exists(),
        "revert": (git_dir / "REVERT_HEAD").exists(),
    }


def build_report(custom_branch: str) -> Dict[str, Any]:
    repository_root = Path(
        first_line(run_git("rev-parse", "--show-toplevel"), "repository root")
    ).resolve()
    upstream_url = first_line(
        run_git("remote", "get-url", "upstream", cwd=repository_root), "upstream URL"
    )
    origin_url = first_line(
        run_git("remote", "get-url", "origin", cwd=repository_root), "origin URL"
    )
    upstream_id = remote_identity(upstream_url)
    origin_id = remote_identity(origin_url)
    if not re.search(r"(?:^|/)github\.com/wei-shaw/sub2api$", upstream_id):
        raise RuntimeError(
            f"upstream must point to Wei-Shaw/sub2api; found: {upstream_url}"
        )
    if origin_id == upstream_id:
        raise RuntimeError("origin and upstream resolve to the same repository")

    official = official_branch(repository_root)
    if not test_ref(repository_root, f"refs/heads/{custom_branch}"):
        raise RuntimeError(f"Custom branch does not exist locally: {custom_branch}")

    status_lines = run_git(
        "status", "--short", "--untracked-files=normal", cwd=repository_root
    )
    current_version, base_version = parse_version(repository_root)
    proposed_version, proposed_tag = next_custom_version(repository_root, base_version)
    origin_custom_head = None
    if test_ref(repository_root, f"refs/remotes/origin/{custom_branch}"):
        origin_custom_head = first_line(
            run_git(
                "rev-parse", f"refs/remotes/origin/{custom_branch}", cwd=repository_root
            ),
            "origin custom branch",
        )

    return {
        "repository_root": repository_root.as_posix(),
        "remotes": {"upstream": upstream_url, "origin": origin_url},
        "branch": {
            "current": first_line(
                run_git("branch", "--show-current", cwd=repository_root),
                "current branch",
            ),
            "expected_custom": custom_branch,
            "head": first_line(run_git("rev-parse", "HEAD", cwd=repository_root), "HEAD"),
            "origin_custom_head": origin_custom_head,
            "official": official,
            "upstream_official_head": first_line(
                run_git(
                    "rev-parse", f"refs/remotes/upstream/{official}", cwd=repository_root
                ),
                "upstream official branch",
            ),
        },
        "working_tree": {
            "clean": not status_lines,
            "tracked_changes": [line for line in status_lines if not line.startswith("??")],
            "untracked_changes": [line for line in status_lines if line.startswith("??")],
        },
        "operation_in_progress": operation_state(repository_root),
        "version": {
            "current_version": current_version,
            "official_base": base_version,
            "proposed_version": proposed_version,
            "proposed_tag": proposed_tag,
        },
        "stashes": run_git("stash", "list", cwd=repository_root),
        "note": "Read-only snapshot. Fetch upstream/origin before relying on commit or tag values.",
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--custom-branch", default="custom/upstream-billing")
    args = parser.parse_args()
    try:
        report = build_report(args.custom_branch)
    except (OSError, RuntimeError) as exc:
        print(f"Preflight failed: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
