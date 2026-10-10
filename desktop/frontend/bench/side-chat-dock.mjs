// Electron end-to-end check for the companion session: real Chromium, the real
// dock region, the real + menu, and the real companion panel. The Go tests prove
// the host side; the unit tests prove the pieces. This proves a user can actually
// reach the feature from the dock and that the panel renders the child session.
//
// Run: node bench/side-chat-dock.mjs --electron
import assert from "node:assert/strict";
import { copyFile, mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const { _electron } = await import("playwright");
const electronHome = await mkdtemp(path.join(tmpdir(), "reasonix-side-chat-"));

const server = await createServer({
  root: frontendDir,
  server: { host: "127.0.0.1", port: 0, hmr: false },
  logLevel: "error",
  plugins: [{
    name: "side-chat-fixture",
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        if (req.url !== "/side-chat-fixture") return next();
        res.setHeader("Content-Type", "text/html");
        res.end(await server.transformIndexHtml(req.url, '<html><head><link rel="icon" href="data:,"></head><body><div id="root"></div><script type="module" src="/bench/side-chat-dock-fixture.tsx"></script></body></html>'));
      });
    },
  }],
});
await server.listen();

let electronApp;

/**
 * An isolated worktree may not have the shell's dependencies installed, while the
 * developer's main checkout still does; REASONIX_ELECTRON_MODULE points at the
 * node_modules that holds the electron package in that case.
 */
function electronExecutable() {
  try {
    return createRequire(path.join(frontendDir, "../electron/package.json"))("electron");
  } catch (error) {
    const fallback = process.env.REASONIX_ELECTRON_MODULE;
    if (!fallback) throw new Error(`electron is not installed beside this worktree; set REASONIX_ELECTRON_MODULE to a node_modules holding it (${error.message})`);
    return createRequire(path.join(fallback, "package.json"))("electron");
  }
}

try {
  const address = server.httpServer.address();
  const url = `http://127.0.0.1:${address.port}/side-chat-fixture`;
  const main = path.join(electronHome, "main.cjs");
  await copyFile(path.join(frontendDir, "bench/transcript-layout-electron.cjs"), main);
  electronApp = await _electron.launch({
    executablePath: electronExecutable(),
    args: [main],
    env: { ...process.env, REASONIX_LAYOUT_URL: url },
  });
  const page = await electronApp.firstWindow();
  page.on("pageerror", (error) => console.error("PAGEERROR", error.message));
  page.on("console", (message) => { if (message.type() === "error") console.error("CONSOLE", message.text()); });
  await page.waitForFunction(() => Boolean(window.__activityBarStore));

  // The user opens the dock's own + menu, the same one that lists files,
  // changes, overview and browser.
  await page.evaluate(() => window.__activityBarStore.getState().setAddMenuOpen(true));
  await page.waitForSelector(".tab-add-menu", { timeout: 10_000 });
  const menuLabels = await page.locator(".tab-add-menu__item").allInnerTexts();
  assert.ok(
    menuLabels.some((label) => label.trim() === "辅助对话"),
    `the + menu offers the companion entry at the same level as the file surfaces; got ${JSON.stringify(menuLabels)}`,
  );

  await page.getByRole("menuitem", { name: "辅助对话" }).click();

  // The companion panel appears in the dock, backed by the child session the host
  // returned.
  await page.waitForSelector('[data-side-chat-tab-id="child-1"]', { timeout: 15_000 });
  const calls = await page.evaluate(() => window.__sideChatCalls);
  assert.deepEqual(calls, ["open:parent-1"], `the host opened a companion for the parent tab; got ${JSON.stringify(calls)}`);

  const tabLabels = await page.locator(".tab-container [role='tab'], .tab-container button").allInnerTexts();
  assert.ok(
    tabLabels.some((label) => label.includes("辅助对话 1")),
    `the dock tab carries the companion ordinal; got ${JSON.stringify(tabLabels)}`,
  );

  // Closing the tab goes back through the host teardown instead of dropping the
  // session silently.
  await page.evaluate(() => {
    const store = window.__activityBarStore.getState();
    const tab = store.tabs.find((candidate) => candidate.type === "sideChat");
    if (tab) void import("/src/lib/sideChatOpen.ts").then((module) => module.closeSideChatTab(tab.id));
  });
  await page.waitForFunction(
    () => window.__sideChatCalls.includes("close:child-1"),
    undefined,
    { timeout: 10_000 },
  );
  const afterClose = await page.evaluate(() => window.__sideChatCalls);
  assert.deepEqual(afterClose, ["open:parent-1", "close:child-1"], `closing reaches the child session; got ${JSON.stringify(afterClose)}`);

  console.log("side-chat dock: + menu entry opens the companion, the panel renders it, and closing tears it down through the host");
} finally {
  await electronApp?.close().catch(() => {});
  await server.close();
  await rm(electronHome, { recursive: true, force: true }).catch(() => {});
}
