// The npm bin: runs the binary in MISHMAR_BIN with the arguments, and never itself.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { chmod, copyFile, mkdir, mkdtemp, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";
import { findBinary } from "../src/index.ts";
import { resolveBinary } from "../src/wrapper.ts";

const here = path.dirname(fileURLToPath(import.meta.url));
const bin = path.join(here, "..", "src", "bin.ts");

let dir: string;
let saved: { bin: string | undefined; path: string | undefined; home: string | undefined };

before(async () => {
  dir = await mkdtemp(path.join(tmpdir(), "mishmar-bin-test-"));
  saved = { bin: process.env.MISHMAR_BIN, path: process.env.PATH, home: process.env.HOME };
});

after(async () => {
  for (const [k, v] of [["MISHMAR_BIN", saved.bin], ["PATH", saved.path], ["HOME", saved.home]] as const) {
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
  await rm(dir, { recursive: true, force: true });
});

/** A fake binary: a script that echoes its arguments and exits with `code`. */
async function script(name: string, code: number): Promise<string> {
  const p = path.join(dir, name);
  await writeFile(p, `#!/bin/sh\necho "$@"\nexit ${code}\n`);
  await chmod(p, 0o755);
  return p;
}

/** A native executable named mishmar in its own directory: a copy of /bin/sh. */
async function native(name: string): Promise<string> {
  const d = path.join(dir, name);
  await mkdir(d, { recursive: true });
  const p = path.join(d, "mishmar");
  await copyFile("/bin/sh", p);
  await chmod(p, 0o755);
  return p;
}

test("the bin runs the binary in MISHMAR_BIN with the arguments and its exit code", async () => {
  const fake = await script("fake", 7);
  const home = path.join(dir, "home");
  await mkdir(home, { recursive: true });
  const { stdout, code } = await new Promise<{ stdout: string; code: number }>((resolve) =>
    execFile(
      process.execPath,
      [bin, "mcp", "--config", "x.json"],
      { env: { ...process.env, MISHMAR_BIN: fake, HOME: home, PATH: path.join(dir, "nowhere") } },
      (err, stdout) => resolve({ stdout, code: (err as { code?: number } | null)?.code ?? 0 }),
    ),
  );
  assert.equal(stdout, "mcp --config x.json\n");
  assert.equal(code, 7);
});

test("the PATH scan skips scripts and the wrapper itself", async () => {
  delete process.env.MISHMAR_BIN;
  process.env.HOME = path.join(dir, "home2");
  const wrapper = await native("w"); // stands in for this wrapper: excluded by real path, through a symlink
  const link = path.join(dir, "linkdir");
  await mkdir(link, { recursive: true });
  await symlink(wrapper, path.join(link, "mishmar"));
  const scriptDir = path.join(dir, "s");
  await mkdir(scriptDir, { recursive: true });
  await writeFile(path.join(scriptDir, "mishmar"), "#!/usr/bin/env node\nprocess.exit(9)\n");
  await chmod(path.join(scriptDir, "mishmar"), 0o755);
  const real = await native("r");

  process.env.PATH = [link, scriptDir, path.dirname(real)].join(path.delimiter);
  assert.equal(await resolveBinary(wrapper), real);
  assert.equal(await findBinary(), path.join(link, "mishmar")); // without the options, the first executable wins

  process.env.PATH = [link, scriptDir].join(path.delimiter);
  // `exclude` holds real paths (the temp dir is a symlink on macOS), as resolveBinary passes them.
  await assert.rejects(findBinary(undefined, { skipScripts: true, exclude: [await realpath(wrapper)] }), /no mishmar binary/);
});
