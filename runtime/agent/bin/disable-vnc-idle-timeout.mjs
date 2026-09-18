import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// KasmVNC 1.5.0's iframe client redirects after 20 idle minutes, independently
// of the server's IdleTimeout. Setting idle_disconnect=0 causes an immediate
// timeout in this version. Retain its keepalive interval without the redirect.
const assets = process.argv[2] ?? "/usr/share/kasmvnc/www/assets";
const clients = readdirSync(assets).filter((name) => /^ui-.*\.js$/.test(name));
if (clients.length !== 1) throw new Error("Expected one KasmVNC UI bundle");

const path = join(assets, clients[0]);
const source = readFileSync(path, "utf8");
const interval = /([\w$]+)\._sessionTimeoutInterval=setInterval\(function\(\)\{.*?\},5e3\)/g;
const matches = [...source.matchAll(interval)];
if (matches.length !== 1 || !matches[0][0].includes("Idle session timeout exceeded")) {
  throw new Error("KasmVNC idle timeout implementation changed; review before building");
}
const ui = matches[0][1];
writeFileSync(path, source.replace(interval,
  `${ui}._sessionTimeoutInterval=setInterval(function(){if(${ui}.rfb)${ui}.rfb.sendKeepAlive()},5e3)`));
