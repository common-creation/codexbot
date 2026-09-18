#!/bin/sh
set -eu

deploy_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "${deploy_dir}/scripts/_env.sh"
load_codexbot_env "${deploy_dir}"
use_codexbot_docker

: "${CODEXBOT_SHARED_HOME:?set CODEXBOT_SHARED_HOME in ${codexbot_env_file}}"

egress_network="${CODEXBOT_EGRESS_NETWORK:-codexbot-egress}"

if ! docker network inspect "${egress_network}" >/dev/null 2>&1; then
    docker network create \
        --label io.codexbot.managed=true \
        --label io.codexbot.purpose=egress \
        "${egress_network}" >/dev/null
fi

docker compose \
    --env-file "${codexbot_env_file}" \
    -f "${deploy_dir}/compose.yaml" \
    config --quiet

docker compose \
    --env-file "${codexbot_env_file}" \
    -f "${deploy_dir}/compose.yaml" \
    up --detach codexbot runtime-manager

published_address=$(docker compose \
    --env-file "${codexbot_env_file}" \
    -f "${deploy_dir}/compose.yaml" \
    port codexbot 8080)

if [ -z "${published_address}" ]; then
    echo "codexbot is healthy inside Docker but port 8080 was not published" >&2
    exit 1
fi

echo "codexbot is available at http://${published_address}"
