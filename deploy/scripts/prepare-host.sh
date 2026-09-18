#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
deploy_dir=$(dirname "${script_dir}")
. "${script_dir}/_env.sh"
load_codexbot_env "${deploy_dir}"

shared_home="${1:-${CODEXBOT_SHARED_HOME:-}}"
docker_socket="${2:-${CODEXBOT_DOCKER_SOCKET:-/run/user/$(id -u)/docker.sock}}"
allow_rootful="${CODEXBOT_ALLOW_ROOTFUL_DOCKER:-false}"
runtime_token_file=$(resolve_codexbot_deploy_path "${CODEXBOT_RUNTIME_TOKEN_FILE:-./secrets/runtime-token}")
bootstrap_token_file=$(resolve_codexbot_deploy_path "${CODEXBOT_BOOTSTRAP_TOKEN_FILE:-./secrets/bootstrap-token}")

if [ "${runtime_token_file}" = "${bootstrap_token_file}" ]; then
    echo "CODEXBOT_RUNTIME_TOKEN_FILE and CODEXBOT_BOOTSTRAP_TOKEN_FILE must be different files" >&2
    exit 2
fi

case "${allow_rootful}" in
    true|false) ;;
    *) echo "CODEXBOT_ALLOW_ROOTFUL_DOCKER must be true or false" >&2; exit 2 ;;
esac

if [ -z "${shared_home}" ]; then
    echo "CODEXBOT_SHARED_HOME must be set in ${codexbot_env_file} or passed as the first argument" >&2
    exit 2
fi
case "${shared_home}" in
    /*) ;;
    *) echo "shared-home must be an absolute path" >&2; exit 2 ;;
esac
case "${shared_home}" in
    /|/home|/root|/usr|/var|/etc|/opt|/srv)
        echo "refusing broad shared-home path: ${shared_home}" >&2
        exit 2
        ;;
esac
if [ ! -S "${docker_socket}" ]; then
    echo "Docker socket not found: ${docker_socket}" >&2
    exit 1
fi

security_options=$(DOCKER_HOST="unix://${docker_socket}" docker info --format '{{json .SecurityOptions}}')
case "${security_options}" in
    *rootless*) docker_mode=rootless ;;
    *)
        if [ "${allow_rootful}" != "true" ]; then
            echo "the selected Docker daemon is not rootless; set CODEXBOT_ALLOW_ROOTFUL_DOCKER=true only after reviewing the host-root risk" >&2
            exit 1
        fi
        docker_mode=rootful
        echo "warning: using a rootful Docker daemon; runtime-manager has effective host-root control" >&2
        ;;
esac

mkdir -p "${shared_home}" "$(dirname -- "${runtime_token_file}")" "$(dirname -- "${bootstrap_token_file}")"

# A dedicated world-searchable/writeable root lets same-UID agent processes
# share files. Never point this at an existing or broad host directory.
chmod 0777 "${shared_home}"
umask 077

create_secret() {
    destination="$1"
    if [ -e "${destination}" ]; then
        if [ ! -f "${destination}" ] || [ ! -s "${destination}" ]; then
            echo "existing secret is not a non-empty regular file: ${destination}" >&2
            exit 1
        fi
        chmod 0600 "${destination}"
        return
    fi
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -base64 48 > "${destination}"
    else
        dd if=/dev/urandom bs=48 count=1 2>/dev/null | base64 > "${destination}"
    fi
    chmod 0600 "${destination}"
}

create_secret "${runtime_token_file}"
create_secret "${bootstrap_token_file}"

echo "host runtime prepared"
echo "shared home: ${shared_home}"
echo "Docker socket: ${docker_socket} (${docker_mode})"
echo "runtime token: ${runtime_token_file}"
echo "bootstrap token: ${bootstrap_token_file}"
