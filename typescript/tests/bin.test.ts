// The npm bin: runs the binary in MISHMAR_BIN with the arguments, and never itself.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { access, chmod, copyFile, mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";
import { RELEASE, findBinary, platformName } from "../src/index.ts";
import { main, olderRelease, resolveBinary } from "../src/wrapper.ts";

const here = path.dirname(fileURLToPath(import.meta.url));
const bin = path.join(here, "..", "src", "bin.ts");

let dir: string;
let saved: { bin: string | undefined; path: string | undefined; home: string | undefined; fetch: typeof fetch };

before(async () => {
  dir = await mkdtemp(path.join(tmpdir(), "mishmar-bin-test-"));
  saved = { bin: process.env.MISHMAR_BIN, path: process.env.PATH, home: process.env.HOME, fetch: globalThis.fetch };
});

after(async () => {
  for (const [k, v] of [["MISHMAR_BIN", saved.bin], ["PATH", saved.path], ["HOME", saved.home]] as const) {
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
  globalThis.fetch = saved.fetch;
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

/** A home folder of its own, holding the given releases under .mishmar/bin, each a script that writes `ran` beside itself when run. */
async function homeWith(name: string, versions: string[]): Promise<string> {
  const home = path.join(dir, name);
  for (const v of versions) {
    const d = path.join(home, ".mishmar", "bin", v);
    await mkdir(d, { recursive: true });
    await writeFile(path.join(d, "mishmar"), `#!/bin/sh\ntouch "${d}/ran"\n`);
    await chmod(path.join(d, "mishmar"), 0o755);
  }
  return home;
}

/** Stands in for the network: serves `RELEASE` as a release whose archive holds a script, or fails every request with `fail`; records the URLs asked for. */
async function fakeReleases(fail?: number): Promise<string[]> {
  const src = await mkdtemp(path.join(dir, "release-"));
  await writeFile(path.join(src, "mishmar"), "#!/bin/sh\necho pinned\n");
  await chmod(path.join(src, "mishmar"), 0o755);
  const name = `mishmar_${RELEASE}_${platformName()}.tar.gz`;
  const file = path.join(src, name);
  await new Promise<void>((resolve, reject) =>
    execFile("tar", ["-czf", file, "-C", src, "mishmar"], { env: { ...process.env, PATH: saved.path } }, (err) => (err ? reject(err) : resolve())),
  );
  const archive = await readFile(file);
  const sums = `${createHash("sha256").update(archive).digest("hex")}  ${name}\n`;
  const fetched: string[] = [];
  globalThis.fetch = (async (url: string | URL | Request) => {
    const u = String(url);
    fetched.push(u);
    if (fail) return new Response("no", { status: fail });
    if (u.endsWith(`/${RELEASE}/SHA256SUMS`)) return new Response(sums);
    if (u.endsWith(`/${RELEASE}/${name}`)) return new Response(archive);
    return new Response("not found", { status: 404 });
  }) as typeof fetch;
  return fetched;
}

/** A PATH with the tools these tests and downloadBinary use, and no mishmar. */
async function toolsOnly(): Promise<string> {
  const tools = path.join(dir, "tools");
  await mkdir(tools, { recursive: true });
  for (const name of ["tar", "gzip", "touch", "sleep"]) {
    const found = (saved.path ?? "").split(path.delimiter).map((d) => path.join(d, name));
    for (const c of found) {
      try {
        await access(c);
        await symlink(c, path.join(tools, name)).catch(() => {});
        break;
      } catch {
        // not in this folder
      }
    }
  }
  return tools;
}

/** No binary in MISHMAR_BIN or on the PATH. */
async function noBinaryFound(home: string) {
  delete process.env.MISHMAR_BIN;
  process.env.PATH = await toolsOnly();
  process.env.HOME = home;
}

test("an older release under ~/.mishmar/bin is not run: the wrapper fetches its pinned one", async () => {
  const home = await homeWith("home-old", ["v0.0.1"]);
  await noBinaryFound(home);
  const fetched = await fakeReleases();
  const lines: string[] = [];
  const got = await resolveBinary(path.join(dir, "self"), (l) => lines.push(l));
  const pinned = path.join(home, ".mishmar", "bin", RELEASE, "mishmar");
  assert.equal(got, pinned);
  await access(pinned);
  assert.ok(fetched.length === 2 && fetched.every((u) => u.includes(`/${RELEASE}/`)), fetched.join(" "));
  assert.deepEqual(lines, [`mishmar: fetching release ${RELEASE} for ${platformName()} into ~/.mishmar/bin/${RELEASE}`]);
});

test("the pinned release already under ~/.mishmar/bin is run without a download, and no other release is", async () => {
  const home = await homeWith("home-pinned", ["v0.0.1", RELEASE, "v99.0.0"]);
  await noBinaryFound(home);
  const fetched = await fakeReleases(500);
  const lines: string[] = [];
  assert.equal(await resolveBinary(path.join(dir, "self"), (l) => lines.push(l)), path.join(home, ".mishmar", "bin", RELEASE, "mishmar"));
  assert.deepEqual(fetched, []);
  assert.deepEqual(lines, []);
});

test("unchanged: MISHMAR_BIN, then a binary on the PATH, is run without a download", async () => {
  const home = await homeWith("home-found", ["v0.0.1"]);
  process.env.HOME = home;
  const fetched = await fakeReleases(500);
  const onPath = await native("onpath");
  process.env.PATH = path.dirname(onPath);
  const fake = await script("fake-bin", 0);
  process.env.MISHMAR_BIN = fake;
  assert.equal(await resolveBinary(path.join(dir, "self")), fake);
  delete process.env.MISHMAR_BIN;
  assert.equal(await resolveBinary(path.join(dir, "self")), onPath);
  assert.deepEqual(fetched, []);
});

test("when the download fails the wrapper stops, says how to retry, and runs no older release", async () => {
  const home = await homeWith("home-fail", ["v0.0.1"]);
  await noBinaryFound(home);
  delete process.env.MISHMAR_WRAPPED;
  await fakeReleases(503);
  const lines: string[] = [];
  const write = process.stderr.write;
  process.stderr.write = ((chunk: string | Uint8Array) => {
    lines.push(String(chunk));
    return true;
  }) as typeof process.stderr.write;
  let code: number;
  try {
    code = await main(pathToFileURL(path.join(dir, "self")).href, ["mcp"]);
  } finally {
    process.stderr.write = write;
  }
  assert.equal(code, 1);
  const err = lines.join("");
  assert.match(err, new RegExp(`could not fetch release ${RELEASE.replaceAll(".", "\\.")} \\(.*HTTP 503\\); run the same command again to retry, or install ${RELEASE.replaceAll(".", "\\.")} by hand from https://github\\.com/mishmar-io/mishmar/releases and put it on the PATH or in MISHMAR_BIN`));
  await assert.rejects(access(path.join(home, ".mishmar", "bin", "v0.0.1", "ran")));
  await assert.rejects(access(path.join(home, ".mishmar", "bin", RELEASE, "mishmar")));
});

/** A stand-in binary in MISHMAR_BIN whose `version` runs `body`; the lines the wrapper logs for it. */
async function logsFor(name: string, body: string): Promise<string[]> {
  const p = path.join(dir, name);
  await writeFile(p, `#!/bin/sh\n${body}\n`);
  await chmod(p, 0o755);
  process.env.MISHMAR_BIN = p;
  process.env.PATH = await toolsOnly();
  process.env.HOME = await homeWith(`home-${name}`, []);
  await fakeReleases(500);
  const lines: string[] = [];
  assert.equal(await resolveBinary(path.join(dir, "self"), (l) => lines.push(l)), p);
  return lines;
}

test("a binary older than the pinned release gets one line on stderr", async () => {
  const lines = await logsFor("old", 'echo "mishmar v0.0.1 (go1.26 linux/amd64)"');
  assert.deepEqual(lines, [
    `mishmar: ${path.join(dir, "old")} is v0.0.1, older than this package's ${RELEASE}; update it (https://docs.mishmar.io/start/install-the-cli/) or remove it to use ${RELEASE}`,
  ]);
});

test("no line for the same or a newer release, or a binary whose version fails, hangs or says something else", async () => {
  const [, major, minor, patch] = /^v(\d+)\.(\d+)\.(\d+)$/.exec(RELEASE) as RegExpExecArray;
  for (const v of [RELEASE, `v${major}.${minor}.1${patch}`, `v${major}.1${minor}.0`, `v1${major}.0.0`]) {
    assert.deepEqual(await logsFor(`v-${v}`, `echo "mishmar ${v} (go1.26 linux/amd64)"`), [], v);
  }
  assert.deepEqual(await logsFor("fails", 'echo "mishmar v0.0.1 (go1.26 linux/amd64)"; exit 1'), []);
  assert.deepEqual(await logsFor("dev", 'echo "mishmar dev (go1.26 linux/amd64)"'), []);
  assert.deepEqual(await logsFor("pseudo", 'echo "mishmar v0.0.1-0.20261110120000-abcdef123456 (go1.26 linux/amd64)"'), []);
  assert.deepEqual(await logsFor("nonsense", "echo hello"), []);
  const start = Date.now();
  assert.deepEqual(await logsFor("hangs", "exec sleep 10"), []);
  assert.ok(Date.now() - start < 6000, `took ${Date.now() - start} ms`);
});

test("releases compare number by number", () => {
  assert.equal(olderRelease("v0.5.9", "v0.5.10"), true);
  assert.equal(olderRelease("v0.5.10", "v0.5.9"), false);
  assert.equal(olderRelease("v0.9.9", "v0.10.0"), true);
  assert.equal(olderRelease("v0.5.3", "v0.5.3"), false);
  assert.equal(olderRelease("v1.0.0", "v0.99.99"), false);
  assert.equal(olderRelease("dev", "v0.5.3"), false);
  assert.equal(olderRelease("v0.5.3", "dev"), false);
});
