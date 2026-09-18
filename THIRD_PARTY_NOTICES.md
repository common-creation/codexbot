# Third-party notices

Codexbot's source repository does not grant a project-wide license unless a
separate license file is added by the copyright holder. Runtime images contain
third-party software under their own terms.

Notable pinned components include:

- KasmVNC 1.5.0 — GPL-2.0. Distributors of a built agent image must satisfy the
  corresponding source and notice obligations. Source:
  <https://github.com/kasmtech/KasmVNC/tree/v1.5.0>
- Playwright Core 1.62.0 and its Docker seccomp profile — Apache-2.0. Source:
  <https://github.com/microsoft/playwright>
  The local seccomp profile additionally allows `faccessat2` for compatibility
  with host runc/libpathrs during container initialization and returns `ENOSYS`
  for `clone3` so glibc can fall back to `clone` when creating threads.
- OpenAI Codex CLI 0.152.1 — Apache-2.0. Source:
  <https://github.com/openai/codex>
- shadcn/ui components in `web/src/components/ui` — MIT. Source:
  <https://github.com/shadcn-ui/ui>. The copyright notice and license are
  included in `web/src/components/ui/LICENSE.md`.
- React, Vite, Vitest, Chromium, XFCE, Squid, Debian packages, Go modules, and
  npm packages — see `go.sum`, `web/package-lock.json`, and the package metadata
  installed into the images for their individual licenses.

This notice is informational and is not a substitute for reviewing the exact
license texts when publishing images or binaries.
