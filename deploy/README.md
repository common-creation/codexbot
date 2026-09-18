# Self-host deployment

This deployment uses an existing **rootless Docker daemon by default**. The
runtime-manager gets that daemon's Unix socket and refuses a rootful daemon
unless `CODEXBOT_ALLOW_ROOTFUL_DOCKER=true` is explicitly set for both host
preparation and Compose. It is the only service with Docker access; neither the
web/API service nor an agent receives the socket.

## First start

1. Choose the Docker daemon. Rootless Docker is strongly recommended: install
   it for the account that will run Codexbot, enable its user service, and
   confirm `docker info` lists `rootless` under Security Options. If using a
   rootful daemon, set `CODEXBOT_ALLOW_ROOTFUL_DOCKER=true` in `deploy/.env` and
   follow the explicit rootful preparation command below.
2. Copy `.env.example` to `.env`. Set the actual rootless socket and a new,
   dedicated absolute shared-home path. `CODEXBOT_SECURE_COOKIES=false` is only
   appropriate for local loopback HTTP; use `true` behind HTTPS.
3. Prepare directories and secrets:

   ```sh
   ./deploy/scripts/prepare-host.sh
   ```

   The script loads `deploy/.env` (or `CODEXBOT_ENV_FILE`) and uses
   `CODEXBOT_SHARED_HOME`, `CODEXBOT_DOCKER_SOCKET`, the two configured secret
   file paths, and `CODEXBOT_ALLOW_ROOTFUL_DOCKER`. Optional positional values
   remain available only as one-run shared-home/socket overrides.

   To deliberately use a rootful daemon instead:

   ```sh
   ./deploy/scripts/prepare-host.sh
   ```

   This assumes `CODEXBOT_ALLOW_ROOTFUL_DOCKER=true` and
   `CODEXBOT_DOCKER_SOCKET=/run/docker.sock` are already set in `deploy/.env`,
   so the same values also reach the runtime-manager after Compose starts.

4. Build all four images, then start the control services:

   ```sh
   ./deploy/scripts/build-images.sh
   ./deploy/scripts/up.sh
   ```

5. Open `http://127.0.0.1:8080` and use the generated bootstrap token from
   `deploy/secrets/bootstrap-token` exactly once to create the local admin.

The `codexbot` API is the only host-published port. It joins a non-internal
`edge` network for Docker port publishing and the internal `control` network for
Runtime Manager RPC. Runtime Manager joins only `control`. Agent-worker and
KasmVNC ports are reached through authenticated runtime-manager proxies.
The Control Plane entrypoint starts as root only long enough to read the
`0600` Compose file secrets, then uses `setpriv` to switch to UID/GID 10001 and
clear every capability before starting the Go service. Its temporary
`DAC_READ_SEARCH`, `SETUID`, `SETGID`, and `SETPCAP` capabilities exist only in
that entrypoint phase; `setpriv` clears the resulting capability bounding set.

## Runtime networking

`up.sh` creates the non-internal `codexbot-egress` network. For each agent,
runtime-manager must create a distinct `--internal` network containing the
agent and its Squid proxy, and connect only that Squid container to
`codexbot-egress`. The agent must not be attached directly to the egress network.
Squid denies loopback, private, link-local, documentation/reserved, multicast,
and cloud-metadata destinations after DNS resolution.

The runtime-manager container itself runs as container UID 0 because a rootless
daemon presents its socket as owned by that UID. This still maps to the
unprivileged host account. Setting `CODEXBOT_ALLOW_ROOTFUL_DOCKER=true` removes
that protection and gives runtime-manager effective host-root control; it is an
explicit break-glass option, not a supported production default.
Runtime-manager's entrypoint temporarily receives `DAC_READ_SEARCH` and
`SETPCAP` to read its `0600` file secret and clear its bounding set; the Go
runtime-manager process runs with no effective or bounded capabilities.

## Migrating from per-agent Codex homes

Deployments created before shared `~/.codex` used a `codex/` directory inside
each per-agent profile volume. Those directories are not merged automatically:
multiple auth files, installation IDs, and SQLite/WAL files cannot be combined
safely. Stop active runs and human desktop leases, back up the shared home and
profile volumes, rebuild the Agent and Runtime Manager images, and recreate each
Agent container. The database migration prevents existing Conversations from
resuming against the now-missing private state; start a new chat, then
authenticate once through Connections to populate the shared `~/.codex`.

Old profile-volume `codex/` directories are no longer read. Remove them only
after the shared login and required new conversations have been verified; an
old Codex thread may depend on state from its former private home.

## Desktop session inactivity

Desktop viewing has no inactivity timeout. The Agent image removes KasmVNC
1.5.0's embedded-client idle redirect while preserving its five-second keepalive.
The server idle timeout and automatic shutdown timers are also disabled.
The build fails if the bundled client changes and the patch needs review.

To apply this to an existing deployment, rebuild the Agent image and recreate
Agent containers while preserving their profile volumes and shared home.
Reload the desktop page to load the updated client.

## OCI startup failures with runc/libpathrs

If agent startup returns `OCI runtime start failed: container process is already
dead`, inspect the affected container's logs and state first. On a host with
runc 1.5.1 and libpathrs 0.2.5, the previous seccomp profile reproduced a SIGSEGV
in `pathrs_reopen` before the agent entrypoint ran, even with `/bin/true` as the
entrypoint. Allowing `faccessat2`, as Docker's default profile does, resolved
that reproduction. The local profile includes this permission; keep the other
seccomp restrictions in place.

The profile also returns `ENOSYS` for `clone3`, allowing glibc to fall back to
`clone`. Returning the default `EPERM` instead can make XFCE abort with
`pthread_create: Operation not permitted`, followed by `KasmVNC exited during
startup`. A successful `/bin/true` probe alone does not cover desktop startup;
verify the actual agent start API and container health after recreation.

The control plane allows 90 seconds for agent-start requests; ordinary runtime
requests retain their 30-second timeout. Runtime Manager bounds worker readiness
to 45 seconds, with each health request limited to 2 seconds.

The profile is embedded in the Runtime Manager image and stored by Docker when
an agent container is created. To apply this change to an existing deployment:

1. Rebuild and redeploy Runtime Manager using the deployment's configured
   Docker daemon and environment.
2. Stop any active run for the affected agent, then stop and remove only its
   `codexbot-agent-<uuid>` container (use `docker rm` without `-v`).
3. Start the agent through Codexbot so Runtime Manager creates it with the new
   profile. Verify worker and desktop readiness.

Keep the agent's named profile volume, shared-home directory, network, and
egress container. Restarting the existing agent container alone does not update
its seccomp profile. This diagnosis applies to the `pathrs_reopen` SIGSEGV;
other OCI errors require their own log inspection.

## Reverse proxy and TLS

For non-loopback access, put an HTTPS reverse proxy in front of port 8080, set
`CODEXBOT_SECURE_COOKIES=true`, preserve WebSocket upgrade headers, and impose
request/time limits appropriate for long-lived event and KasmVNC streams. Do not
publish port 8081, 8082, or 6901.

## Persistence and backup

Back up all three persistence classes together:

- `codexbot_codexbot-state`: SQLite state.
- The configured host shared-home directory: files and `~/.codex`
  authentication/configuration/session state visible to every agent.
- Docker volumes named `codexbot-agent-<uuid>-profile`: per-agent browser login
  state, XFCE state, KasmVNC state, and worker outbox.

Stop the control plane and agent containers, or use a filesystem/database-aware
snapshot, before taking a consistent backup. Secrets under `deploy/secrets` are
not included in images and must be backed up separately with mode `0600`.

`down.sh` stops the Compose services without deleting volumes. It intentionally
does not delete dynamically created agent containers, profiles, networks, the
egress network, or shared-home data.
