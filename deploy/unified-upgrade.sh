#!/usr/bin/env bash
set -euo pipefail

# Root-only release procedure for the binance unified runtime. Secrets are read
# from existing systemd environment files and are never echoed or committed.
app_root=/opt/Price-Prediction-Using-Liquidation-Levels
source_root="$app_root/source"
data_root=/var/lib/liquidation-predictor
legacy_root=/opt/tv_okx_bot
legacy_state=/var/lib/tv-okx-bot
revision="${1:?usage: unified-upgrade.sh <binance-revision> [build-branch]}"
build_branch="${2:-binance}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
short_revision="${revision:0:12}"
backup="$data_root/backups/$stamp-$short_revision"
binary="$app_root/liquidation-predictor"
new_binary="$app_root/liquidation-predictor.new"

test -d "$source_root/.git"
test -f "$data_root/liquidation.db"
mkdir -p "$backup"

backup_file() {
  local source="$1" name="$2"
  if [[ -f "$source" ]]; then
    install -o root -g root -m 0600 "$source" "$backup/$name"
  fi
}

backup_sqlite() {
  local source="$1" name="$2"
  if [[ -f "$source" ]]; then
    python3 - "$source" "$backup/$name" <<'PY'
import sqlite3, sys
source = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
destination = sqlite3.connect(sys.argv[2])
with destination:
    source.backup(destination)
destination.close(); source.close()
PY
    chmod 0600 "$backup/$name"
  fi
}

backup_sqlite "$data_root/liquidation.db" liquidation.db
backup_sqlite "$legacy_root/data/tvbot.db" tvbot.db
backup_file "$legacy_root/data/orders.json" tvbot-orders.json
backup_file "$legacy_root/data/binance-credentials.json" tvbot-binance-credentials.json
backup_file "$legacy_state/okx-credentials.json" tvbot-okx-credentials.json
backup_file "$legacy_state/upgrade-status.json" tvbot-upgrade-status.json
backup_file /etc/tv-okx-bot/config.json tvbot-config.json
backup_file "$binary" liquidation-predictor
sha256sum "$backup"/* > "$backup/sha256.txt"

git -C "$source_root" fetch origin binance
if git -C "$source_root" show-ref --verify --quiet refs/heads/binance; then
  git -C "$source_root" switch binance
else
  git -C "$source_root" switch -c binance --track origin/binance
fi
git -C "$source_root" merge --ff-only origin/binance
test "$(git -C "$source_root" rev-parse HEAD)" = "$revision"
test -z "$(git -C "$source_root" status --porcelain)"
git -C "$source_root" diff --check
chown -R liquidation:liquidation "$app_root"

if [[ ! -f "$data_root/tvbot-config.json" ]]; then
  install -o liquidation -g liquidation -m 0600 /etc/tv-okx-bot/config.json "$data_root/tvbot-config.json"
fi
if [[ -f "$legacy_root/data/binance-credentials.json" && ! -f "$data_root/binance-credentials.json" ]]; then
  install -o liquidation -g liquidation -m 0600 "$legacy_root/data/binance-credentials.json" "$data_root/binance-credentials.json"
fi
if [[ -f "$legacy_state/okx-credentials.json" && ! -f "$data_root/okx-credentials.json" ]]; then
  install -o liquidation -g liquidation -m 0600 "$legacy_state/okx-credentials.json" "$data_root/okx-credentials.json"
fi

cd "$source_root"
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildRevision=$revision -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildBranch=$build_branch" -o "$new_binary" ./cmd/server
install -o liquidation -g liquidation -m 0755 "$new_binary" "$binary"
rm -f "$new_binary"

install -o root -g root -m 0644 deploy/liquidation-predictor.service /etc/systemd/system/liquidation-predictor.service
install -o root -g root -m 0644 deploy/liquidation-predictor.env /etc/liquidation-predictor.env
install -o root -g root -m 0440 deploy/liquidation-predictor-upgrade.sudoers /etc/sudoers.d/liquidation-predictor-upgrade
install -o root -g root -m 0644 deploy/nginx-liquidation-locations.conf /etc/nginx/snippets/liquidation-locations.conf
install -o root -g root -m 0644 deploy/nginx-tvbot.lmitis.com /etc/nginx/sites-enabled/tv-okx-bot.conf
visudo -cf /etc/sudoers.d/liquidation-predictor-upgrade
nginx -t
systemctl daemon-reload
systemctl restart liquidation-predictor.service

for _ in $(seq 1 30); do
  if systemctl is-active --quiet liquidation-predictor.service && curl -fsS http://127.0.0.1:9090/healthz >/dev/null && curl -fsS http://127.0.0.1:9090/readyz >/dev/null; then
    printf '%s\n' "$backup" > "$app_root/dashboard-backup-path"
    echo "deployed_revision=$revision"
    echo "backup=$backup"
    systemctl is-active liquidation-predictor.service
    exit 0
  fi
  sleep 2
done

echo "unified service did not become healthy; old tv-okx-bot service remains available for rollback" >&2
systemctl status liquidation-predictor.service --no-pager >&2 || true
exit 1
