"""The console script: runs the binary in MISHMAR_BIN with the arguments, and never itself."""

import os
import shutil
import subprocess
import sys

import pytest

from mishmar import cli
from mishmar.signer import RELEASE, find_binary


@pytest.fixture
def fake(tmp_path):
    """A fake binary: a script that echoes its arguments and exits 7."""
    p = tmp_path / "fake"
    p.write_text('#!/bin/sh\necho "$@"\nexit 7\n')
    p.chmod(0o755)
    return str(p)


def native(tmp_path, name):
    """A native executable named mishmar in its own directory: a copy of /bin/sh."""
    d = tmp_path / name
    d.mkdir()
    p = d / "mishmar"
    shutil.copy("/bin/sh", p)
    p.chmod(0o755)
    return str(p)


def test_the_script_runs_the_binary_in_mishmar_bin(fake, tmp_path):
    env = {
        **os.environ,
        "MISHMAR_BIN": fake,
        "HOME": str(tmp_path / "home"),
        "PATH": str(tmp_path / "nowhere"),
    }
    env.pop("MISHMAR_WRAPPED", None)
    run = subprocess.run(
        [sys.executable, "-m", "mishmar.cli", "mcp", "--config", "x.json"],
        env=env,
        capture_output=True,
        text=True,
    )
    assert run.stdout == "mcp --config x.json\n"
    assert run.returncode == 7


def test_the_path_scan_skips_scripts_and_the_wrapper_itself(tmp_path, monkeypatch):
    monkeypatch.delenv("MISHMAR_BIN", raising=False)
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    wrapper = native(
        tmp_path, "w"
    )  # stands in for this wrapper: excluded by real path, through a symlink
    link = tmp_path / "linkdir"
    link.mkdir()
    (link / "mishmar").symlink_to(wrapper)
    script_dir = tmp_path / "s"
    script_dir.mkdir()
    (script_dir / "mishmar").write_text("#!/usr/bin/env python3\nraise SystemExit(9)\n")
    (script_dir / "mishmar").chmod(0o755)
    real = native(tmp_path, "r")

    monkeypatch.setenv(
        "PATH", os.pathsep.join([str(link), str(script_dir), os.path.dirname(real)])
    )
    assert cli.resolve_binary(wrapper) == real
    assert find_binary() == str(
        link / "mishmar"
    )  # without the options, the first executable wins

    monkeypatch.setenv("PATH", os.pathsep.join([str(link), str(script_dir)]))
    with pytest.raises(FileNotFoundError):
        find_binary(skip_scripts=True, exclude={wrapper})


def test_resolve_fetches_the_pinned_release_when_there_is_none(tmp_path, monkeypatch):
    monkeypatch.delenv("MISHMAR_BIN", raising=False)
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.setenv("PATH", str(tmp_path / "nowhere"))
    fetched = []
    monkeypatch.setattr(
        cli, "download_binary", lambda version: fetched.append(version) or "/dl/mishmar"
    )
    lines = []
    assert cli.resolve_binary(str(tmp_path / "self"), lines.append) == "/dl/mishmar"
    assert fetched == [RELEASE]
    assert lines and RELEASE in lines[0]


def test_a_wrapper_started_by_a_wrapper_stops(monkeypatch, capsys):
    monkeypatch.setenv("MISHMAR_WRAPPED", "1")
    assert cli.main(["mcp"]) == 2
    assert "MISHMAR_BIN" in capsys.readouterr().err
