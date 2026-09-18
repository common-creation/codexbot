#!/bin/sh

# Shared environment loader for host-side deployment scripts. The env file is
# administrator-owned and intentionally uses POSIX shell-compatible syntax.
load_codexbot_env() {
    codexbot_deploy_dir="$1"
    codexbot_env_file="${CODEXBOT_ENV_FILE:-${codexbot_deploy_dir}/.env}"

    if [ ! -f "${codexbot_env_file}" ]; then
        echo "environment file not found: ${codexbot_env_file}" >&2
        exit 1
    fi

    set -a
    # shellcheck disable=SC1090
    . "${codexbot_env_file}"
    set +a
}

use_codexbot_docker() {
    : "${CODEXBOT_DOCKER_SOCKET:?set CODEXBOT_DOCKER_SOCKET in ${codexbot_env_file}}"
    if [ ! -S "${CODEXBOT_DOCKER_SOCKET}" ]; then
        echo "Docker socket not found: ${CODEXBOT_DOCKER_SOCKET}" >&2
        exit 1
    fi
    export DOCKER_HOST="unix://${CODEXBOT_DOCKER_SOCKET}"
}

resolve_codexbot_deploy_path() {
    case "$1" in
        /*) printf '%s\n' "$1" ;;
        ./*) printf '%s/%s\n' "${codexbot_deploy_dir}" "${1#./}" ;;
        *) printf '%s/%s\n' "${codexbot_deploy_dir}" "$1" ;;
    esac
}
