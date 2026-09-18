# Sandbox and native approval checks

The agent uses Codex CLI 0.153.4. Its sandbox command is `codex sandbox`,
without a `linux` subcommand.

Run the offline regression check inside an existing agent:

```sh
docker exec -i CONTAINER python3 - < runtime/agent/tests/codex-sandbox.py
```

It uses separate temporary configuration and checks workspace editing,
read-only mode, protected `.git`/`.codex`/`.agents` paths, writes outside the
workspace, and network isolation against a reachable outer-container listener.
It makes no model calls and does not inspect the real Codex configuration or
user files.

The opt-in live test checks native review of an escalated command and a
synthetic MCP tool. It requires an existing file-based Codex login, uses model
calls, and writes only disposable test markers:

```sh
docker exec -i -e CODEXBOT_NATIVE_REVIEW_LIVE=1 CONTAINER python3 - \
  < runtime/agent/tests/native-auto-review.py
```

It uses a temporary workspace/configuration and references the existing login
only for authentication. The configured browser/desktop MCP servers are not
loaded. `CODEXBOT_NATIVE_REVIEW_MODEL` optionally selects the working model;
Codex itself selects the approval reviewer. The test requires native approved
notifications for both actions, verifies their markers, and fails if any
manual approval request reaches the client. It does not test live unsafe
actions or guarantee every future review decision.

The container keeps its read-only root filesystem, non-root user,
no-new-privileges, and explicit seccomp profile. Bubblewrap additionally needs
`mount`, `umount2`, and `pivot_root` in its nested namespace. No `SYS_ADMIN`
capability or unconfined seccomp profile is required on the tested host.
Other hosts can impose additional AppArmor/user-namespace restrictions; the
entrypoint runs a real Codex sandbox preflight and refuses startup on failure.

References: [Bubblewrap #505](https://github.com/containers/bubblewrap/issues/505),
[Codex #29908](https://github.com/openai/codex/issues/29908), and
[Codex Auto-review](https://learn.chatgpt.com/docs/sandboxing/auto-review).
