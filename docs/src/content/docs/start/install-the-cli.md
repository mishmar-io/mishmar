---
title: Install the mishmar CLI
description: Download the mishmar binary for macOS or Linux, check it against the release's checksums, and put it on the PATH of the machine your agent runs on. Update an existing install the same way.
sidebar:
  order: 1
lastUpdated: 2026-11-17
---

The `mishmar` binary is the agent's side of Mishmar. It writes the agent's signer config, makes the agent's half of each trading key with Mishmar, runs the signer, and can propose orders from the command line. The Python and TypeScript SDKs run the same binary as a child process.

Install it on the machine your agent runs on. That machine holds the agent's half of every trading key, so it should be one you control.

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
- **AI apps (Claude Code, Cursor and the others).** Add the server to your app again and restart the app. This is required: an app you added before v0.6.0 keeps running the old version. The command is on Trade from Claude Code, Cursor or another AI app, and it names the current package. If you never installed `mishmar` yourself, you don't download anything: the package fetches the release it pins. If you put `mishmar` on your PATH yourself, the app runs that copy: do steps 1 to 4, then restart the app. Either way, the first Mishmar tool the app uses replaces the older background signer by itself. To check, run `npx -y mishmar@0.6 daemon status`: it prints the version that runs.

## When something goes wrong

- **macOS says the developer cannot be verified.** The file was downloaded with a browser, which marks it as quarantined. Download it with `curl` as above, or clear the mark: `xattr -d com.apple.quarantine /usr/local/bin/mishmar`.
- **`command not found: mishmar`.** `/usr/local/bin` is not on your PATH. Move the binary to a directory that is (`echo $PATH` lists them), or call it by its full path.
- **The SDKs cannot find the binary.** They look in `$MISHMAR_BIN`, then on the PATH, then under `~/.mishmar/bin/<version>/`. Set `MISHMAR_BIN` to the binary's full path if it lives somewhere else.
