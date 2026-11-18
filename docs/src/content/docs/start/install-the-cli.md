---
title: Install the mishmar CLI
description: Download the mishmar binary for macOS or Linux, check it against the release's checksums, and put it on the PATH of the machine your agent runs on. Update an existing install the same way.
sidebar:
  order: 1
lastUpdated: 2026-11-19
---

The `mishmar` binary is the agent's side of Mishmar. It writes the agent's signer config, makes the agent's half of each trading key with Mishmar, runs the signer, and can propose orders from the command line. The Python and TypeScript SDKs run the same binary as a child process.

Install it on the machine your agent runs on. That machine holds the agent's half of every trading key, so it should be one you control.

## v0.6.0 pre-release (testnet only)

Mishmar's testnet now accepts only mishmar v0.6.0 or later. Production still accepts v0.5.4 until v0.6.0 is released there, planned for 20 or 21 November. If you use production only, change nothing yet.

v0.6.0 is a pre-release, for testnet only. It is not marked as the latest release on GitHub, so the steps further down this page still install {{MISHMAR_VERSION}}. Do not use v0.6.0 with production or with real funds until it is released for production.

Install only from this page. We never send a file or a download link by email or chat.

1. Download the archive for your machine, and the checksums. Inside WSL 2, your machine is Linux.

   ```sh
   OS=$(uname -s | tr '[:upper:]' '[:lower:]')            # darwin or linux
   ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')  # amd64 or arm64
   NAME=mishmar_v0.6.0_${OS}_${ARCH}.tar.gz
   curl -fsSLO https://github.com/mishmar-io/mishmar/releases/download/v0.6.0/$NAME
   curl -fsSLO https://github.com/mishmar-io/mishmar/releases/download/v0.6.0/SHA256SUMS
   ```

   The same files as links: [macOS, Apple silicon](https://github.com/mishmar-io/mishmar/releases/download/v0.6.0/mishmar_v0.6.0_darwin_arm64.tar.gz), [macOS, Intel](https://github.com/mishmar-io/mishmar/releases/download/v0.6.0/mishmar_v0.6.0_darwin_amd64.tar.gz), [Linux, x86_64](https://github.com/mishmar-io/mishmar/releases/download/v0.6.0/mishmar_v0.6.0_linux_amd64.tar.gz), [Linux, arm64](https://github.com/mishmar-io/mishmar/releases/download/v0.6.0/mishmar_v0.6.0_linux_arm64.tar.gz) and [SHA256SUMS](https://github.com/mishmar-io/mishmar/releases/download/v0.6.0/SHA256SUMS). They are the files of the [v0.6.0 release](https://github.com/mishmar-io/mishmar/releases/tag/v0.6.0) on GitHub.

2. Check the archive against the checksums. Do not run it if this does not print `OK`.

   On macOS:

   ```sh
   grep " $NAME\$" SHA256SUMS | shasum -a 256 -c -
   ```

   On Linux, and inside WSL 2:

   ```sh
   grep " $NAME\$" SHA256SUMS | sha256sum -c -
   ```

   ```text
   mishmar_v0.6.0_darwin_arm64.tar.gz: OK
   ```

3. Unpack it and put the binary on your PATH, over the older one if there is one.

   ```sh
   tar -xzf $NAME
   sudo mv mishmar /usr/local/bin/
   ```

4. Check it worked, then start the signer as usual. A signer that is running still runs the older version: stop it (Ctrl-C in its terminal) first.

   ```sh
   mishmar version
   mishmar signer run
   ```

   The first command prints `mishmar v0.6.0 (…)`.

AI apps (Claude Code, Cursor and the others): the package your app runs, `mishmar@0.6`, is published on the day production moves. Until then your AI app cannot trade on testnet. It keeps working on production, so use production in the meantime.

## What you need

- macOS or Linux, on amd64 (x86_64) or arm64. There is no Windows build yet. On Windows, run the CLI inside [WSL 2](https://learn.microsoft.com/windows/wsl/install) (Windows Subsystem for Linux) and follow the Linux steps.
- `curl`, `tar`, and `shasum` (macOS, most Linux systems) or `sha256sum`.

## Steps

1. Pick the release. The command below names the latest, {{MISHMAR_VERSION}}; the [releases page](https://github.com/mishmar-io/mishmar/releases) lists them all, and the archives are named `mishmar_<version>_<os>_<arch>.tar.gz`.

   ```sh
   VERSION={{MISHMAR_VERSION}}
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
   mishmar_{{MISHMAR_VERSION}}_darwin_arm64.tar.gz: OK
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

## Update an existing install

See which version you have:

```sh
mishmar version
```

It prints the version, then the Go version and the platform it was built for: `mishmar v… (go… darwin/arm64)`.

To update, do steps 1 to 4 again. Step 1 names the newest release, {{MISHMAR_VERSION}}, and step 4 moves the new binary over the old one in `/usr/local/bin`. If you put the old one somewhere else, put the new one at the same path. Then `mishmar version` prints the new version.

Updating does not touch `~/.mishmar/agent.json`, which holds the agent's key and its half of each trading key. The new version reads the same file.

A signer that is running keeps running the old version: nothing restarts it. Stop it (Ctrl-C in its terminal) and start it again:

```sh
mishmar signer run
```

If something else starts the binary for you:

- **The Python and TypeScript SDKs** run the first binary they find: `$MISHMAR_BIN`, then the one on the PATH, then the newest under `~/.mishmar/bin/`. Upgrading the SDK package does not replace a binary they already find. After steps 1 to 4, restart your bot, and the signer it starts is the new version.
- **The AI apps' `npx -y mishmar@0.5 mcp --app …`** (and `uvx`) runs the mishmar on your PATH when there is one. After steps 1 to 4, restart the app: the first Mishmar tool it uses then replaces the older background signer by itself. `npx -y mishmar@0.5 daemon status` prints the version that runs.

## When something goes wrong

- **macOS says the developer cannot be verified.** The file was downloaded with a browser, which marks it as quarantined. Download it with `curl` as above, or clear the mark: `xattr -d com.apple.quarantine /usr/local/bin/mishmar`.
- **`command not found: mishmar`.** `/usr/local/bin` is not on your PATH. Move the binary to a directory that is (`echo $PATH` lists them), or call it by its full path.
- **The SDKs cannot find the binary.** They look in `$MISHMAR_BIN`, then on the PATH, then under `~/.mishmar/bin/<version>/`. Set `MISHMAR_BIN` to the binary's full path if it lives somewhere else.
