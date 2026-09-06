#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
/usr/bin/git -c 'url.git@github.com:.insteadOf=' -c 'url.ssh://git@github.com/.insteadOf=' push origin v0.3.0-lark-streaming