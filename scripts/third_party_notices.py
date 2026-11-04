"""Write the licenses of everything linked into the mishmar binary.

The release puts the output beside the binary in every archive. It lists the
Go modules ./cmd/mishmar imports for the platform it runs on, the Go runtime,
the cb-mpc-go fork, the cb-mpc library inside it and OpenSSL, with each
module's license and NOTICE files, one copy of each distinct text.

    python3 scripts/third_party_notices.py --openssl 3.6.1 > dist/THIRD_PARTY_NOTICES

Run it from the repository root with ../cb-mpc-go checked out, as the build is.
"""

import argparse
import hashlib
import os
import re
import subprocess
import sys
from pathlib import Path

# Files a module ships its terms in. NOTICE files are kept too: Apache-2.0
# requires them to travel with the work.
TERMS = re.compile(r"^(licen[cs]e|copying|notice)([.-].*)?$", re.IGNORECASE)

OPENSSL = """OpenSSL {version}
Copyright (c) 1998-2025 The OpenSSL Project Authors
Copyright (c) 1995-1998 Eric A. Young, Tim J. Hudson
Licensed under the Apache License 2.0, the same text as mishmar's own LICENSE
in this archive. https://www.openssl.org/source/license.html
"""


def terms(directory: Path) -> list[Path]:
    """The license and notice files at the top of a module."""
    return sorted(p for p in directory.iterdir() if p.is_file() and TERMS.match(p.name))


def modules() -> list[tuple[str, Path]]:
    """Every module the binary imports, but mishmar itself: name@version and its directory."""
    out = subprocess.run(
        [
            "go",
            "list",
            "-deps",
            "-f",
            "{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}}\t"
            "{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}",
            "./cmd/mishmar",
        ],
        check=True,
        capture_output=True,
        text=True,
        env={**os.environ, "CGO_ENABLED": "1"},
    ).stdout
    found = {}
    for line in out.splitlines():
        if line:
            name, directory = line.split("\t")
            found[name] = Path(directory)
    return sorted(found.items())


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--openssl", required=True, help="the OpenSSL version linked in, such as 3.6.1"
    )
    args = parser.parse_args()

    goroot = Path(
        subprocess.run(
            ["go", "env", "GOROOT"], check=True, capture_output=True, text=True
        ).stdout.strip()
    )
    parts: list[tuple[str, list[Path]]] = [
        ("Go (the standard library and runtime)", terms(goroot))
    ]
    for name, directory in modules():
        parts.append(
            (
                name.replace(
                    "@v0.0.0-00010101000000-000000000000", " (the mishmar-io fork)"
                ),
                terms(directory),
            )
        )
        if name.startswith("github.com/mishmar-io/cb-mpc-go@"):
            parts.append(
                (
                    "cb-mpc (Coinbase, linked through cb-mpc-go)",
                    terms(directory / "cb-mpc"),
                )
            )

    missing = [name for name, files in parts if not files]
    if missing:
        print("no license file found for: " + ", ".join(missing), file=sys.stderr)
        return 1

    # One copy of each distinct text, under the names of everything it covers.
    texts: dict[str, tuple[list[str], str]] = {}
    for name, files in parts:
        body = "\n\n".join(f.read_text(errors="replace").strip() for f in files)
        key = hashlib.sha256(body.encode()).hexdigest()
        texts.setdefault(key, ([], body))[0].append(name)

    rule = "=" * 78
    print("The mishmar binary includes the following software. Each part lists the")
    print("components it covers, then their license and notice text.\n")
    for names, body in texts.values():
        print(rule)
        print("\n".join(names))
        print("-" * 78)
        print(body + "\n")
    print(rule)
    print(OPENSSL.format(version=args.openssl))
    return 0


if __name__ == "__main__":
    sys.exit(main())
