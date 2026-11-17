/**
 * `mishmar` as an npm bin: `npx -y mishmar mcp`, or any other mishmar
 * command, without installing the CLI first. It runs `MISHMAR_BIN`, or a
 * `mishmar` on the PATH less any wrapper such as this one, and otherwise
 * exactly the release this package pins, `~/.mishmar/bin/<release>`, fetched
 * first when it is not there; other releases under `~/.mishmar/bin` are never
 * used. It runs the binary with the same arguments, stdin, stdout and stderr,
 * so stdout stays the MCP channel; it writes nothing there itself. It forwards
 * SIGINT and SIGTERM and exits with the binary's code.
 */

import { execFile, spawn } from "node:child_process";
import { realpath } from "node:fs/promises";
import { homedir, constants as osConstants } from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { RELEASE, downloadBinary, exe, executable, findBinary, platformName } from "./signer.ts";

/** Set on the binary this wrapper starts; a wrapper that finds it set has been started by another wrapper, which means it resolved to itself. */
const ENV_WRAPPED = "MISHMAR_WRAPPED";
const INSTALL_DOCS = "https://docs.mishmar.io/start/install-the-cli/";
const RELEASES_PAGE = "https://github.com/mishmar-io/mishmar/releases";

/** A release's three numbers from `v1.2.3`, or undefined for anything else. */
function releaseNumbers(version: string): [number, number, number] | undefined {
  const m = /^v(\d+)\.(\d+)\.(\d+)$/.exec(version);
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : undefined;
}

/** Whether release `version` comes before release `than`, number by number (v0.5.9 before v0.5.10); false when either is not a release. */
export function olderRelease(version: string, than: string): boolean {
  const a = releaseNumbers(version);
  const b = releaseNumbers(than);
  if (!a || !b) return false;
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return (a[i] as number) < (b[i] as number);
  return false;
}

/** The release `binary version` prints (`mishmar v0.5.3 (go1.26 linux/amd64)`), or undefined when it fails, takes over 2 s or prints anything else. */
async function binaryVersion(binary: string): Promise<string | undefined> {
  return new Promise((resolve) =>
    execFile(binary, ["version"], { timeout: 2000, windowsHide: true }, (err, stdout) => {
      const m = err ? null : /^mishmar (v\d+\.\d+\.\d+) \(/.exec(String(stdout));
      resolve(m ? m[1] : undefined);
    }),
  );
}

/**
 * The binary for the wrapper at `self` (the running script's path) to run:
 * `MISHMAR_BIN` or one on the PATH, else the pinned release, fetched into
 * `~/.mishmar/bin/<release>` when it is not there. `log` gets one line when a
 * download starts, and one when the binary found is an older release than
 * the pinned one. Throws, saying how to retry, when the download fails.
 */
export async function resolveBinary(self: string, log: (line: string) => void = () => {}): Promise<string> {
  const exclude = [await realpath(self).catch(() => self)];
  let found: string | undefined;
  try {
    found = await findBinary(undefined, { skipScripts: true, exclude, skipDownloaded: true });
  } catch {
    // none in MISHMAR_BIN or on the PATH
  }
  if (found !== undefined) {
    const version = await binaryVersion(found);
    if (version && olderRelease(version, RELEASE)) {
      log(`mishmar: ${found} is ${version}, older than this package's ${RELEASE}; update it (${INSTALL_DOCS}) or remove it to use ${RELEASE}`);
    }
    return found;
  }
  const pinned = path.join(homedir(), ".mishmar", "bin", RELEASE, exe("mishmar"));
  if (await executable(pinned)) return pinned;
  log(`mishmar: fetching release ${RELEASE} for ${platformName()} into ~/.mishmar/bin/${RELEASE}`);
  try {
    return await downloadBinary(RELEASE);
  } catch (err) {
    throw new Error(
      `could not fetch release ${RELEASE} (${err instanceof Error ? err.message : String(err)}); run the same command again to retry, or install ${RELEASE} by hand from ${RELEASES_PAGE} and put it on the PATH or in MISHMAR_BIN`,
    );
  }
}

/** Run the binary with `argv`; the exit code to end this process with. */
export async function main(selfUrl: string, argv: string[] = process.argv.slice(2)): Promise<number> {
  const stderr = (line: string) => process.stderr.write(line + "\n");
  if (process.env[ENV_WRAPPED]) {
    stderr("mishmar: this wrapper was started by another wrapper, so the PATH holds no mishmar binary; set MISHMAR_BIN to it");
    return 2;
  }
  let binary: string;
  try {
    binary = await resolveBinary(fileURLToPath(selfUrl), stderr);
  } catch (err) {
    stderr(`mishmar: ${err instanceof Error ? err.message : String(err)}`);
    return 1;
  }
  const child = spawn(binary, argv, { stdio: "inherit", env: { ...process.env, [ENV_WRAPPED]: "1" } });
  const forward = (signal: NodeJS.Signals) => child.kill(signal);
  process.on("SIGINT", forward);
  process.on("SIGTERM", forward);
  return new Promise((resolve) => {
    child.on("error", (err) => {
      stderr(`mishmar: ${binary}: ${err.message}`);
      resolve(1);
    });
    child.on("exit", (code, signal) => resolve(code ?? (signal ? 128 + (osConstants.signals[signal] ?? 0) : 1)));
  });
}
