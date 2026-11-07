#!/usr/bin/env node
// `mishmar` on npm: runs the real binary with these arguments (see wrapper.ts).
import { main } from "./wrapper.ts";

process.exitCode = await main(import.meta.url);
