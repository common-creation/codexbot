import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import fs from "node:fs/promises";
import vm from "node:vm";
import test from "node:test";

// Run the actual stdio server with only its external dependencies replaced.
// This needs neither an installed Playwright package nor a desktop session.
const source = (await fs.readFile(new URL("../bin/browser-mcp.mjs", import.meta.url), "utf8"))
  .replace(/^import .*;\n/gm, "");

function harness(connect) {
  let now = 0;
  const calls = [];
  const stdin = new EventEmitter();
  stdin.setEncoding = () => {};
  const process = new EventEmitter();
  process.stdin = stdin;
  process.stdout = { write() {} };
  process.env = {};
  const sandbox = vm.createContext({
    createRequire: () => () => ({ chromium: {
      connectOverCDP: async (endpoint, options) => {
        calls.push({ endpoint, options });
        return connect(options, (elapsed) => { now += elapsed; });
      },
    } }),
    fs: { readFile: async () => JSON.stringify({ holder: "agent", generation: 1 }) },
    process,
    performance: { now: () => now },
    setTimeout: (callback, delay) => { now += delay; queueMicrotask(callback); },
  });
  vm.runInContext(source, sandbox);
  return {
    context: vm.runInContext("context", sandbox),
    snapshot: () => vm.runInContext("callTool('browser_snapshot', {})", sandbox),
    calls,
    elapsed: () => now,
  };
}

function visibleBrowser() {
  const browser = new EventEmitter();
  browser.isConnected = () => true;
  const page = {
    setDefaultTimeout() {},
    locator: () => ({ innerText: async () => "日本語のテスト" }),
    url: () => "https://example.com/",
    title: async () => "日本語",
  };
  const context = { pages: () => [page] };
  browser.contexts = () => [context];
  return { browser, context };
}

test("unavailable browser fails within five seconds with launch instructions", async () => {
  const server = harness(async ({ timeout }, advance) => {
    advance(timeout);
    throw new Error("connection timed out");
  });
  await assert.rejects(server.snapshot(), /desktop_open_app \(app: "browser"\)/);
  assert.equal(server.elapsed(), 5_000);
  assert.equal(server.calls.length, 4);
  for (const { endpoint, options } of server.calls) {
    assert.equal(endpoint, "http://127.0.0.1:9222");
    assert.ok(options.timeout > 0 && options.timeout <= 1_000);
  }
});

test("a failed snapshot can succeed after the visible browser is opened", async () => {
  let available = false;
  const { browser } = visibleBrowser();
  const server = harness(async () => {
    if (!available) throw new Error("ECONNREFUSED");
    return browser;
  });
  await assert.rejects(server.snapshot(), /ECONNREFUSED/);
  assert.equal(server.elapsed(), 5_000);
  available = true;
  const result = await server.snapshot();
  assert.equal(JSON.parse(result.content[0].text).text, "日本語のテスト");
});

test("concurrent calls share a connection and reconnect after disconnect", async () => {
  const first = visibleBrowser();
  const second = visibleBrowser();
  let activeBrowser = first.browser;
  const server = harness(async () => activeBrowser);
  const results = await Promise.all([server.context(), server.context()]);
  assert.equal(results[0], first.context);
  assert.equal(results[1], first.context);
  assert.equal(server.calls.length, 1);
  activeBrowser = second.browser;
  first.browser.emit("disconnected");
  assert.equal(await server.context(), second.context);
  assert.equal(server.calls.length, 2);
  // A stale disconnect from the previous transport must not evict the new one.
  first.browser.emit("disconnected");
  assert.equal(await server.context(), second.context);
  assert.equal(server.calls.length, 2);
});
