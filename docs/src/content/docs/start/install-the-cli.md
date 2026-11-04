---
title: Install the mishmar CLI
description: Download the mishmar binary for macOS or Linux, check it against the release's checksums, and put it on the PATH of the machine your agent runs on.
sidebar:
  order: 1
lastUpdated: 2026-11-04
---

The `mishmar` binary is the agent's side of Mishmar. It writes the agent's signer config, makes the agent's half of each trading key with Mishmar, runs the signer, and can propose orders from the command line. The Python and TypeScript SDKs run the same binary as a child process.

Install it on the machine your agent runs on. That machine holds the agent's half of every trading key, so it should be one you control.

## What you need

- macOS or Linux, on amd64 (x86_64) or arm64. There is no Windows build yet.
- `curl`, `tar`, and `shasum` (macOS, most Linux systems) or `sha256sum`.

## Steps

1. Pick the release. The [releases page](https://github.com/mishmar-io/mishmar/releases) lists them; the archives are named `mishmar_<version>_<os>_<arch>.tar.gz`.

   ```sh
   VERSION=v0.2.0
   OS=$(uname -s | tr '[:upper:]' '[:lower:]')            # darwin or linux
   ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')  # amd64 or arm64
   NAME=mishmar_${VERSION}_${OS}_${ARCH}.tar.gz
   ```

2. Download the archive and the checksums.

   ```sh
   curl -fsSLO https://github.com/mishmar-io/mishmar/releases/download/$VERSION/$NAME
   curl -fsSLO https://github.com/mishmar-io/mishmar/releases/download/$VERSION/SHA256SUMS
   ```

3. Check the archive against the checksums. Stop here if this does not print `OK`.

   ```sh
   grep " $NAME\$" SHA256SUMS | shasum -a 256 -c -    # or: sha256sum -c -
   ```

   ```text
   mishmar_v0.2.0_darwin_arm64.tar.gz: OK
   ```

4. Unpack it and put the binary on your PATH.

   ```sh
   tar -xzf $NAME
   sudo mv mishmar /usr/local/bin/
   ```

   The archive also holds `LICENSE` and `THIRD_PARTY_NOTICES`, the licenses of mishmar and of the libraries built into it.

## Check it worked

```sh
mishmar version
```

It prints the version you downloaded. `mishmar --help` lists every command, grouped by who runs them.

## When something goes wrong

- **macOS says the developer cannot be verified.** The file was downloaded with a browser, which marks it as quarantined. Download it with `curl` as above, or clear the mark: `xattr -d com.apple.quarantine /usr/local/bin/mishmar`.
- **`command not found: mishmar`.** `/usr/local/bin` is not on your PATH. Move the binary to a directory that is (`echo $PATH` lists them), or call it by its full path.
- **The SDKs cannot find the binary.** They look in `$MISHMAR_BIN`, then on the PATH, then under `~/.mishmar/bin/<version>/`. Set `MISHMAR_BIN` to the binary's full path if it lives somewhere else.
