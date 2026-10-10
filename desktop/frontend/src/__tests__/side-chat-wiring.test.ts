// Run: tsx src/__tests__/side-chat-wiring.test.ts
//
// Wiring check for the companion session. The host exposes two side-chat
// commands in the generated desktop contract; the backend keeps them honest
// through TestHostCommandOwnersMatchSource. Nothing checked the other end: a
// command can be implemented and reachable in Go while no renderer path ever
// calls it, which is exactly how the + menu entry and the dock launcher entry
// were missing while the backend was fully live. This test fails when a
// generated side-chat command loses its production consumer.

import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve } from "node:path";

const testDir = import.meta.dirname;
const sourceRoot = resolve(testDir, "..");
const contract = readFileSync(resolve(sourceRoot, "generated/desktopContract.generated.ts"), "utf8");

// The generated command list is the single source of truth for what the host
// offers; filter it down to the companion surface instead of hard-coding names.
const commandsBlock = /export const DESKTOP_COMMANDS = \[([\s\S]*?)\];/.exec(contract)?.[1] ?? "";
const sideChatCommands = [...commandsBlock.matchAll(/"([A-Za-z0-9_]*SideChat[A-Za-z0-9_]*)"/g)].map((match) => match[1]);
assert.ok(sideChatCommands.length >= 2, `expected the contract to expose the side-chat commands, got ${JSON.stringify(sideChatCommands)}`);

/**
 * Production sources only. The generated contract and the bridge describe the
 * commands; the attachment/property mocks replay them for the browser build. A
 * command that only appears in those files has no renderer path behind it.
 */
const EXCLUDED = [
  resolve(sourceRoot, "generated"),
  resolve(sourceRoot, "lib/bridge.ts"),
  resolve(sourceRoot, "__tests__"),
  resolve(sourceRoot, "test-support"),
];

function productionSources(dir: string, found: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (EXCLUDED.some((excluded) => path === excluded || path.startsWith(excluded + (process.platform === "win32" ? "\\" : "/")))) continue;
    if (statSync(path).isDirectory()) productionSources(path, found);
    else if (/\.tsx?$/.test(entry) && !/\.test\.tsx?$/.test(entry)) found.push(path);
  }
  return found;
}

const sources = productionSources(sourceRoot).map((path) => ({ path, text: readFileSync(path, "utf8") }));

let failed = 0;
for (const command of sideChatCommands) {
  const consumers = sources.filter((source) => source.text.includes(`.${command}(`));
  if (consumers.length === 0) {
    failed += 1;
    console.error(`  FAIL  ${command} has no production renderer consumer — the backend is reachable but no UI path calls it`);
  } else {
    console.log(`  PASS  ${command} is consumed by ${consumers.map((source) => source.path.slice(sourceRoot.length + 1)).join(", ")}`);
  }
}

assert.equal(failed, 0, "every generated side-chat command needs a production consumer");
console.log(`\nside-chat wiring: ${sideChatCommands.length} commands consumed`);
