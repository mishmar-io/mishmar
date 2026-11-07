/**
 * `mishmar` as an npm bin: `npx -y mishmar mcp`, or any other mishmar
 * command, without installing the CLI first. It finds the real binary
 * (`MISHMAR_BIN`, the PATH less any wrapper such as this one, then
 * `~/.mishmar/bin`), fetches the pinned release when there is none, and runs
 * it with the same arguments, stdin, stdout and stderr, so stdout stays the
 * MCP channel; it writes nothing there itself. It forwards SIGINT and SIGTERM
 * and exits with the binary's code.
 */

import { spawn } from "node:child_process";
import { realpath } from "node:fs/promises";
import { constants as osConstants } from "node:os";
import { fileURLToPath } from "node:url";
import { RELEASE, downloadBinary, findBinary, platformName } from "./signer.ts";

/** Set on the binary this wrapper starts; a wrapper that finds it set has been started by another wrapper, which means it resolved to itself. */
const ENV_WRAPPED = "MISHMAR_WRAPPED";

/**
 * The binary for the wrapper at `self` (the running script's path) to run:
 * found, or fetched into `~/.mishmar/bin/<release>`. `log` gets one line
 * when a download starts.
 */
export async function resolveBinary(self: string, log: (line: string) => void = () => {}): Promise<string> {
  const exclude = [await realpath(self).catch(() => self)];
  try {
    return await findBinary(undefined, { skipScripts: true, exclude });
  } catch {
    log(`mishmar: fetching release ${RELEASE} for ${platformName()} into ~/.mishmar/bin/${RELEASE}`);
    return await downloadBinary(RELEASE);
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
