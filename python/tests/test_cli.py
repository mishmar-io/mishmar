"""The console script: runs the binary in MISHMAR_BIN with the arguments, and never itself."""

import os
import shutil
import subprocess
import sys
import time

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


def home_with(tmp_path, monkeypatch, *versions):
    """HOME of its own, holding the given releases under .mishmar/bin, each a
    script that writes ``ran`` beside itself when run."""
    home = tmp_path / "home"
    touch = shutil.which("touch")
    for v in versions:
        d = home / ".mishmar" / "bin" / v
        d.mkdir(parents=True)
        (d / "mishmar").write_text(f'#!/bin/sh\n{touch} "{d}/ran"\n')
        (d / "mishmar").chmod(0o755)
    monkeypatch.setenv("HOME", str(home))
    return home


def no_binary_found(monkeypatch, tmp_path):
    monkeypatch.delenv("MISHMAR_BIN", raising=False)
    monkeypatch.setenv("PATH", str(tmp_path / "nowhere"))


def test_an_older_release_is_not_run_the_pinned_one_is_fetched(tmp_path, monkeypatch):
    home_with(tmp_path, monkeypatch, "v0.0.1")
    no_binary_found(monkeypatch, tmp_path)
    fetched = []
    monkeypatch.setattr(
        cli, "download_binary", lambda version: fetched.append(version) or "/dl/mishmar"
    )
    lines = []
    assert cli.resolve_binary(str(tmp_path / "self"), lines.append) == "/dl/mishmar"
    assert fetched == [RELEASE]
    assert len(lines) == 1 and f"fetching release {RELEASE}" in lines[0]


def test_the_pinned_release_already_fetched_is_run_and_no_other(tmp_path, monkeypatch):
    home = home_with(tmp_path, monkeypatch, "v0.0.1", RELEASE, "v99.0.0")
    no_binary_found(monkeypatch, tmp_path)
    fetched = []
    monkeypatch.setattr(cli, "download_binary", fetched.append)
    lines = []
    assert cli.resolve_binary(str(tmp_path / "self"), lines.append) == str(
        home / ".mishmar" / "bin" / RELEASE / "mishmar"
    )
    assert fetched == []
    assert lines == []


def test_unchanged_mishmar_bin_then_the_path_is_run_without_a_download(
    fake, tmp_path, monkeypatch
):
    home_with(tmp_path, monkeypatch, "v0.0.1")
    fetched = []
    monkeypatch.setattr(cli, "download_binary", fetched.append)
    on_path = native(tmp_path, "onpath")
    monkeypatch.setenv("PATH", os.path.dirname(on_path))
    monkeypatch.setenv("MISHMAR_BIN", fake)
    assert cli.resolve_binary(str(tmp_path / "self")) == fake
    monkeypatch.delenv("MISHMAR_BIN")
    assert cli.resolve_binary(str(tmp_path / "self")) == on_path
    assert fetched == []


def test_when_the_download_fails_the_wrapper_stops_and_runs_no_older_release(
    tmp_path, monkeypatch, capsys
):
    home = home_with(tmp_path, monkeypatch, "v0.0.1")
    no_binary_found(monkeypatch, tmp_path)
    monkeypatch.delenv("MISHMAR_WRAPPED", raising=False)

    def fail(version):
        raise OSError("HTTP Error 503: Service Unavailable")

    monkeypatch.setattr(cli, "download_binary", fail)
    ran = []

    def execv(binary, args):
        ran.append(binary)
        raise SystemExit(0)

    monkeypatch.setattr(cli.os, "execv", execv)
    monkeypatch.setattr(cli.subprocess, "call", lambda args: ran.append(args) or 0)
    assert cli.main(["mcp"]) == 1
    assert ran == []
    assert not (home / ".mishmar" / "bin" / "v0.0.1" / "ran").exists()
    out = capsys.readouterr()
    assert out.out == ""
    assert out.err == (
        f"mishmar: fetching release {RELEASE} for {cli.platform_name()} "
        f"into ~/.mishmar/bin/{RELEASE}\n"
        f"mishmar: could not fetch release {RELEASE} (HTTP Error 503: Service "
        f"Unavailable); run the same command again to retry, or install {RELEASE} "
        "by hand from https://github.com/mishmar-io/mishmar/releases and put it on "
        "the PATH or in MISHMAR_BIN\n"
    )


def logs_for(tmp_path, monkeypatch, name, body):
    """A stand-in binary in MISHMAR_BIN whose ``version`` runs ``body``; the
    lines the wrapper logs for it."""
    p = tmp_path / name
    p.write_text(f"#!/bin/sh\n{body}\n")
    p.chmod(0o755)
    monkeypatch.setenv("MISHMAR_BIN", str(p))
    monkeypatch.setattr(cli, "download_binary", lambda version: pytest.fail("fetched"))
    lines = []
    assert cli.resolve_binary(str(tmp_path / "self"), lines.append) == str(p)
    return lines


def test_a_binary_older_than_the_pinned_release_gets_one_line(tmp_path, monkeypatch):
    home_with(tmp_path, monkeypatch)
    lines = logs_for(
        tmp_path, monkeypatch, "old", 'echo "mishmar v0.0.1 (go1.26 linux/amd64)"'
    )
    assert lines == [
        f"mishmar: {tmp_path / 'old'} is v0.0.1, older than this package's {RELEASE}; "
        "update it (https://docs.mishmar.io/start/install-the-cli/) or remove it to "
        f"use {RELEASE}"
    ]


def test_no_line_for_the_same_or_newer_release_or_a_version_that_fails(
    tmp_path, monkeypatch
):
    home_with(tmp_path, monkeypatch)
    major, minor, patch = RELEASE[1:].split(".")
    for i, v in enumerate(
        [
            RELEASE,
            f"v{major}.{minor}.1{patch}",
            f"v{major}.1{minor}.0",
            f"v1{major}.0.0",
        ]
    ):
        body = f'echo "mishmar {v} (go1.26 linux/amd64)"'
        assert logs_for(tmp_path, monkeypatch, f"v{i}", body) == [], v
    for name, body in [
        ("fails", 'echo "mishmar v0.0.1 (go1.26 linux/amd64)"; exit 1'),
        ("dev", 'echo "mishmar dev (go1.26 linux/amd64)"'),
        (
            "pseudo",
            'echo "mishmar v0.0.1-0.20261110120000-abcdef123456 (go1.26 linux/amd64)"',
        ),
        ("nonsense", "echo hello"),
    ]:
        assert logs_for(tmp_path, monkeypatch, name, body) == [], name
    start = time.monotonic()
    hangs = f"exec {shutil.which('sleep')} 10"
    assert logs_for(tmp_path, monkeypatch, "hangs", hangs) == []
    assert time.monotonic() - start < 6


def test_releases_compare_number_by_number():
    assert cli.older_release("v0.5.9", "v0.5.10")
    assert not cli.older_release("v0.5.10", "v0.5.9")
    assert cli.older_release("v0.9.9", "v0.10.0")
    assert not cli.older_release("v0.5.3", "v0.5.3")
    assert not cli.older_release("v1.0.0", "v0.99.99")
    assert not cli.older_release("dev", "v0.5.3")
    assert not cli.older_release("v0.5.3", "dev")
