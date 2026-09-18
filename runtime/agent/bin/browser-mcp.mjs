#!/usr/bin/env node
/** Stdio MCP server for the visible, persistent Chromium session. */

import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const require = createRequire("/opt/codexbot/browser-mcp/package.json");
const { chromium } = require("playwright-core");

const serverInfo = { name: "codexbot-browser", version: "0.1.0" };
const leaseFile = process.env.CODEXBOT_DESKTOP_LEASE_FILE || "/var/lib/codexbot/profile/desktop-lease.json";
const generationProperty = { type: "integer", minimum: 1, description: "Generation returned by browser_snapshot or browser_screenshot." };
const tools = [
  {
    name: "browser_navigate",
    description: "Navigate the visible Chromium page to an HTTP or HTTPS URL.",
    inputSchema: {
      type: "object",
      properties: { url: { type: "string", maxLength: 8192 }, generation: generationProperty },
      required: ["url", "generation"],
      additionalProperties: false,
    },
  },
  {
    name: "browser_snapshot",
    description: "Read a concise text snapshot of the visible page.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
  {
    name: "browser_click",
    description: "Click an element selected by CSS, or by ARIA role and accessible name.",
    inputSchema: {
      type: "object",
      properties: {
        selector: { type: "string", maxLength: 2048 },
        role: { type: "string", maxLength: 100 },
        name: { type: "string", maxLength: 500 },
        generation: generationProperty,
      },
      required: ["generation"],
      additionalProperties: false,
    },
  },
  {
    name: "browser_type",
    description: "Fill or type text into an element selected by CSS.",
    inputSchema: {
      type: "object",
      properties: {
        selector: { type: "string", maxLength: 2048 },
        text: { type: "string", maxLength: 20000 },
        submit: { type: "boolean", default: false },
        generation: generationProperty,
      },
      required: ["selector", "text", "generation"],
      additionalProperties: false,
    },
  },
  {
    name: "browser_keypress",
    description: "Send a Playwright key chord such as Control+L or Enter to the page.",
    inputSchema: {
      type: "object",
      properties: { keys: { type: "string", maxLength: 100 }, generation: generationProperty },
      required: ["keys", "generation"],
      additionalProperties: false,
    },
  },
  {
    name: "browser_download",
    description: "Click a CSS-selected download and save it into the shared Downloads directory.",
    inputSchema: {
      type: "object",
      properties: { selector: { type: "string", maxLength: 2048 }, generation: generationProperty },
      required: ["selector", "generation"],
      additionalProperties: false,
    },
  },
  {
    name: "browser_screenshot",
    description: "Capture the visible browser page as a PNG.",
    inputSchema: {
      type: "object",
      properties: { fullPage: { type: "boolean", default: false } },
      additionalProperties: false,
    },
  },
];

let browserPromise;
const connectionBudgetMs = 5_000;

async function context() {
  if (!browserPromise) {
    const pendingConnection = (async () => {
      const deadline = performance.now() + connectionBudgetMs;
      let lastError;
      while (performance.now() < deadline) {
        try {
          const browser = await chromium.connectOverCDP("http://127.0.0.1:9222", {
            timeout: Math.max(1, Math.min(1_000, deadline - performance.now())),
          });
          browser.on("disconnected", () => {
            if (browserPromise === pendingConnection) browserPromise = undefined;
          });
          if (!browser.isConnected()) throw new Error("visible Chromium disconnected during connection");
          return browser;
        } catch (error) {
          lastError = error;
          const remaining = deadline - performance.now();
          if (remaining > 0) await new Promise((resolve) => setTimeout(resolve, Math.min(250, remaining)));
        }
      }
      throw new Error(`visible Chromium debugging endpoint is unavailable; open Chromium with desktop_open_app (app: "browser"), then retry this browser tool: ${lastError?.message || "timeout"}`);
    })();
    browserPromise = pendingConnection;
    // A failed first call must not poison later calls after Chromium starts.
    pendingConnection.catch(() => {
      if (browserPromise === pendingConnection) browserPromise = undefined;
    });
  }
  const browser = await browserPromise;
  const browserContext = browser.contexts()[0];
  if (!browserContext) throw new Error("visible Chromium has no default browser context");
  return browserContext;
}

async function page() {
  const browserContext = await context();
  return browserContext.pages()[0] || browserContext.newPage();
}

function locatorFor(targetPage, args) {
  if (args.selector) return targetPage.locator(args.selector).first();
  if (args.role && args.name) return targetPage.getByRole(args.role, { name: args.name }).first();
  throw new Error("provide selector, or both role and name");
}

function textResult(text) {
  return { content: [{ type: "text", text }] };
}

async function readLease() {
  let lease;
  try {
    lease = JSON.parse(await fs.readFile(leaseFile, "utf8"));
  } catch {
    throw new Error("desktop lease is unavailable");
  }
  if (!["agent", "human"].includes(lease.holder) || !Number.isInteger(lease.generation)) {
    throw new Error("desktop lease is invalid");
  }
  return lease;
}

async function requireAgentLease(args) {
  const lease = await readLease();
  if (lease.holder !== "agent") throw new Error("desktop control is held by a human");
  if (args.generation !== lease.generation) {
    throw new Error("stale desktop lease generation; capture a new snapshot");
  }
  return lease;
}

async function callTool(name, args) {
  if (["browser_navigate", "browser_click", "browser_type", "browser_keypress", "browser_download"].includes(name)) {
    await requireAgentLease(args);
  }

  const targetPage = await page();
  targetPage.setDefaultTimeout(30_000);

  switch (name) {
    case "browser_navigate": {
      const url = new URL(args.url);
      if (url.protocol !== "http:" && url.protocol !== "https:") {
        throw new Error("only http and https URLs are allowed");
      }
      const response = await targetPage.goto(url.toString(), { waitUntil: "domcontentloaded" });
      return textResult(JSON.stringify({ url: targetPage.url(), title: await targetPage.title(), status: response?.status() ?? null }));
    }
    case "browser_snapshot": {
      const bodyText = await targetPage.locator("body").innerText({ timeout: 10_000 }).catch(() => "");
      const lease = await readLease();
      return textResult(JSON.stringify({
        url: targetPage.url(),
        title: await targetPage.title(),
        text: bodyText.slice(0, 50_000),
        desktopLease: lease,
      }));
    }
    case "browser_click":
      await locatorFor(targetPage, args).click();
      return textResult("click completed");
    case "browser_type": {
      const locator = locatorFor(targetPage, args);
      await locator.fill(args.text);
      if (args.submit) await locator.press("Enter");
      return textResult("text entered");
    }
    case "browser_keypress":
      await targetPage.keyboard.press(args.keys);
      return textResult("key press completed");
    case "browser_download": {
      const downloadPromise = targetPage.waitForEvent("download");
      await locatorFor(targetPage, args).click();
      const download = await downloadPromise;
      const safeName = path.basename(download.suggestedFilename());
      const destination = path.join("/home/agent/Downloads", safeName);
      await download.saveAs(destination);
      return textResult(JSON.stringify({ filename: safeName, path: destination }));
    }
    case "browser_screenshot": {
      const data = await targetPage.screenshot({ type: "png", fullPage: Boolean(args.fullPage) });
      const lease = await readLease();
      return { content: [
        { type: "image", data: data.toString("base64"), mimeType: "image/png" },
        { type: "text", text: JSON.stringify({ desktopLease: lease }) },
      ] };
    }
    default:
      throw new Error(`unknown tool: ${name}`);
  }
}

function respond(id, result, error) {
  const response = { jsonrpc: "2.0", id };
  if (error) response.error = error;
  else response.result = result;
  process.stdout.write(`${JSON.stringify(response)}\n`);
}

let pending = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", async (chunk) => {
  pending += chunk;
  let newline;
  while ((newline = pending.indexOf("\n")) >= 0) {
    const line = pending.slice(0, newline).trim();
    pending = pending.slice(newline + 1);
    if (!line) continue;

    let request;
    try {
      request = JSON.parse(line);
    } catch (error) {
      respond(null, null, { code: -32700, message: error.message });
      continue;
    }
    if (request.id === undefined || request.id === null) continue;

    try {
      if (request.method === "initialize") {
        respond(request.id, {
          protocolVersion: request.params?.protocolVersion || "2025-06-18",
          capabilities: { tools: {} },
          serverInfo,
        });
      } else if (request.method === "ping") {
        respond(request.id, {});
      } else if (request.method === "tools/list") {
        respond(request.id, { tools });
      } else if (request.method === "tools/call") {
        try {
          respond(request.id, await callTool(request.params?.name, request.params?.arguments || {}));
        } catch (error) {
          respond(request.id, { content: [{ type: "text", text: error.message }], isError: true });
        }
      } else {
        respond(request.id, null, { code: -32601, message: "method not found" });
      }
    } catch (error) {
      respond(request.id, null, { code: -32603, message: error.message });
    }
  }
});

async function shutdown() {
  // Chromium belongs to the desktop session. Process exit drops the CDP
  // transport without sending Browser.close to the shared visible browser.
  process.exit(0);
}

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
process.stdin.on("end", shutdown);
