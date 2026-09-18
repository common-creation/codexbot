#!/bin/sh
set -eu

deploy_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "${deploy_dir}/scripts/_env.sh"
load_codexbot_env "${deploy_dir}"
use_codexbot_docker

exec docker compose \
    --env-file "${codexbot_env_file}" \
    -f "${deploy_dir}/compose.yaml" \
    down
