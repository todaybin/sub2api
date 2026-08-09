#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$SCRIPT_DIR"

export DATA_DIR="${DATA_DIR:-$SCRIPT_DIR/data}"
export CONFIG_FILE="${CONFIG_FILE:-$SCRIPT_DIR/config.yaml}"
export SKIP_SETUP="${SKIP_SETUP:-true}"

exec "$SCRIPT_DIR/sub2api"
