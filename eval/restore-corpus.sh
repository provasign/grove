#!/usr/bin/env bash
# Restore the eval corpus: clone every benchmarked repo at its pinned commit.
#
# The corpus is reproducible working state (the precious artifacts — truth
# snapshots in eval/testdata/<repo>@<pin>/ and baselines — are committed), so
# it lives outside git. This script rebuilds it after a restart / on a fresh
# machine in one command. Pins MUST match .github/workflows/eval.yml.
#
# Usage:   eval/restore-corpus.sh            # → /tmp/eval-corpus (default)
#          EVAL_CORPUS=~/eval-corpus eval/restore-corpus.sh   # persistent dir
set -euo pipefail

DEST="${EVAL_CORPUS:-/tmp/eval-corpus}"
mkdir -p "$DEST"

# name|url|pinned-commit  (keep in sync with eval.yml *_COMMIT vars)
CORPUS="
gin|https://github.com/gin-gonic/gin.git|d75fcd4
flask|https://github.com/pallets/flask.git|36e4a824
socket.io|https://github.com/socketio/socket.io.git|3ad4e1f2
express|https://github.com/expressjs/express.git|dae209ae
ripgrep|https://github.com/BurntSushi/ripgrep.git|82313cf
newtonsoft|https://github.com/JamesNK/Newtonsoft.Json.git|0a2e291
php-parser|https://github.com/nikic/PHP-Parser.git|8eea230
jansson|https://github.com/akheron/jansson.git|684e18c
"

while IFS='|' read -r name url pin; do
  [ -z "$name" ] && continue
  dir="$DEST/$name"
  if [ -d "$dir/.git" ] && [ "$(git -C "$dir" rev-parse --short HEAD 2>/dev/null)" = "$pin" ]; then
    echo "ok    $name @ $pin (already present)"
    continue
  fi
  echo "clone $name @ $pin"
  rm -rf "$dir"
  git clone --quiet "$url" "$dir"
  git -C "$dir" checkout --quiet "$pin"
done <<< "$CORPUS"

echo "corpus restored to $DEST"
