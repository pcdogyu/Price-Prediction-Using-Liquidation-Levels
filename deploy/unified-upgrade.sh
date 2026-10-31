#!/usr/bin/env bash
set -euo pipefail

# Root-only release procedure for the binance unified runtime. Secrets are read
# from existing systemd environment files and are never echoed or committed.
# The Linux binary is built on the publishing machine and uploaded before this
# script runs; the server never compiles it.
app_root=/opt/Price-Prediction-Using-Liquidation-Levels
source_root="$app_root/source"
data_root=/var/lib/liquidation-predictor
legacy_root=/opt/tv_okx_bot
legacy_state=/var/lib/tv-okx-bot
revision="${1:?usage: unified-upgrade.sh <binance-revision> <build-branch> <uploaded-server-binary> <uploaded-collector-binary> [server-sha256] [collector-sha256]}"
build_branch="${2:-binance}"
artifact="${3:?usage: unified-upgrade.sh <binance-revision> <build-branch> <uploaded-server-binary> <uploaded-collector-binary> [server-sha256] [collector-sha256]}"
collector_artifact="${4:?usage: unified-upgrade.sh <binance-revision> <build-branch> <uploaded-server-binary> <uploaded-collector-binary> [server-sha256] [collector-sha256]}"
expected_sha256="${5:-}"
expected_collector_sha256="${6:-}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
short_revision="${revision:0:12}"
backup="$data_root/backups/$stamp-$short_revision"
backup_retention_days=7
backup_retention_minutes=$((backup_retention_days * 24 * 60))
binary="$app_root/liquidation-predictor"
new_binary="$app_root/liquidation-predictor.new"
collector_binary="$app_root/market-collector"
new_collector_binary="$app_root/market-collector.new"

case "$revision" in
  (*[!0-9a-fA-F]*) echo "invalid revision" >&2; exit 1 ;;
esac
case "$artifact" in
  "$app_root"/liquidation-predictor.upload.*) ;;
  (*) echo "uploaded binary must be staged under $app_root" >&2; exit 1 ;;
esac
case "$collector_artifact" in
  "$app_root"/market-collector.upload.*) ;;
  (*) echo "uploaded collector binary must be staged under $app_root" >&2; exit 1 ;;
esac
test -f "$artifact"
test -f "$collector_artifact"
if [[ -n "$expected_sha256" ]]; then
  actual_sha256="$(sha256sum "$artifact" | awk '{print $1}')"
  test "$actual_sha256" = "$expected_sha256"
fi
if [[ -n "$expected_collector_sha256" ]]; then
  actual_collector_sha256="$(sha256sum "$collector_artifact" | awk '{print $1}')"
  test "$actual_collector_sha256" = "$expected_collector_sha256"
fi
trap 'rm -f -- "$artifact" "$collector_artifact" "$new_binary" "$new_collector_binary"' EXIT

test -d "$source_root/.git"
test -f "$data_root/liquidation.db"
git config --global --add safe.directory "$source_root" || true
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
mkdir -p "$backup"

backup_file() {
  local source="$1" name="$2"
  if [[ -f "$source" ]]; then
    install -o root -g root -m 0600 "$source" "$backup/$name"
  fi
}

backup_tree() {
  local source="$1" target="$2"
  if [[ ! -d "$source" ]]; then
    return 0
  fi
  mkdir -p "$target"
  cp -a "$source"/. "$target"/
  find "$target" -type d -exec chmod 0700 {} +
  find "$target" -type f -exec chmod 0600 {} +
  local files=()
  while IFS= read -r -d '' file; do
    files+=("$file")
  done < <(find "$target" -type f -print0)
  if ((${#files[@]} > 0)); then
    sha256sum "${files[@]}" > "$target.sha256"
  fi
}

backup_file /etc/tv-okx-bot/data/orders.json tvbot-orders.json
backup_file "$legacy_root/data/binance-credentials.json" tvbot-binance-credentials.json
backup_file "$legacy_state/okx-credentials.json" tvbot-okx-credentials.json
backup_file "$legacy_state/upgrade-status.json" tvbot-upgrade-status.json
backup_file /etc/tv-okx-bot/config.json tvbot-config.json
backup_file /etc/liquidation-predictor.env liquidation-predictor.env
backup_file "$binary" liquidation-predictor
backup_file "$collector_binary" market-collector
sha256sum "$backup"/* > "$backup/sha256.txt"
backup_tree "$data_root/users" "$backup/tvbot-user-credentials"

# Database files are live application state and are intentionally not copied
# or snapshotted during deployment. An existing migration file, when present,
# is left untouched and is not recreated by later releases.
echo "database_backup=skipped"

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
install -o liquidation -g liquidation -m 0755 "$artifact" "$new_binary"
mv -f -- "$new_binary" "$binary"
echo "uploaded_binary_sha256=$(sha256sum "$binary" | awk '{print $1}')"
install -o liquidation -g liquidation -m 0755 "$collector_artifact" "$new_collector_binary"
mv -f -- "$new_collector_binary" "$collector_binary"
echo "uploaded_collector_binary_sha256=$(sha256sum "$collector_binary" | awk '{print $1}')"

install -o root -g root -m 0644 deploy/liquidation-predictor.service /etc/systemd/system/liquidation-predictor.service
install -o root -g root -m 0644 deploy/market-collector.service /etc/systemd/system/market-collector.service
install -o root -g root -m 0644 deploy/liquidation-predictor.env /etc/liquidation-predictor.env
if [[ -f "$backup/liquidation-predictor.env" ]]; then
  # Preserve operator-managed authentication and tvorder webhook settings
  # when the checked-in baseline environment file is installed.  The
  # webhook token/nonce are generated credentials and must never be stored in
  # the repository or printed during an upgrade.  Python replaces complete
  # lines so secret values cannot be corrupted by sed delimiters or escapes.
  python3 - "$backup/liquidation-predictor.env" /etc/liquidation-predictor.env <<'PY'
import os
import sys
import tempfile
from pathlib import Path

source = Path(sys.argv[1])
target = Path(sys.argv[2])
keys = {
    "APP_AUTH_USERNAME",
    "APP_AUTH_PASSWORD_HASH",
    "APP_STEADY_WEBHOOK_URL",
    "APP_STEADY_WEBHOOK_API_ID",
    "APP_STEADY_WEBHOOK_TARGET_EXCHANGE",
    "APP_STEADY_WEBHOOK_TRADE_ENV",
    "APP_STEADY_WEBHOOK_EXCHANGE",
    "APP_STEADY_WEBHOOK_INTERVAL",
    "APP_STEADY_WEBHOOK_TOKEN_NONCE",
    "APP_STEADY_WEBHOOK_TOKEN",
}
values = {}
for line in source.read_text(encoding="utf-8").splitlines():
    if "=" not in line:
        continue
    key, value = line.split("=", 1)
    if key in keys:
        values[key] = value

lines = []
seen = set()
for line in target.read_text(encoding="utf-8").splitlines():
    key = line.split("=", 1)[0] if "=" in line else ""
    if key in values:
        lines.append(f"{key}={values[key]}")
        seen.add(key)
    else:
        lines.append(line)
for key, value in values.items():
    if key not in seen:
        lines.append(f"{key}={value}")

fd, temp_name = tempfile.mkstemp(prefix="liquidation-predictor.env.", dir=str(target.parent), text=True)
try:
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        handle.write("\n".join(lines) + "\n")
    os.chown(temp_name, 0, 0)
    os.chmod(temp_name, 0o644)
    os.replace(temp_name, target)
finally:
    try:
        os.unlink(temp_name)
    except FileNotFoundError:
        pass
PY
fi
install -o root -g root -m 0440 deploy/liquidation-predictor-upgrade.sudoers /etc/sudoers.d/liquidation-predictor-upgrade
install -o root -g root -m 0644 deploy/nginx-liquidation-locations.conf /etc/nginx/snippets/liquidation-locations.conf
install -o root -g root -m 0644 deploy/nginx-tvbot.lmitis.com /etc/nginx/sites-enabled/tv-okx-bot.conf
visudo -cf /etc/sudoers.d/liquidation-predictor-upgrade
nginx -t
systemctl daemon-reload
systemctl restart market-collector.service
systemctl restart liquidation-predictor.service

prune_old_backups() {
  local old_count remaining_count
  old_count="$(find "$data_root/backups" -mindepth 1 -maxdepth 1 -type d -name '20??????T??????Z-*' -mmin "+$backup_retention_minutes" -print | wc -l)"
  if ((old_count > 0)); then
    find "$data_root/backups" -mindepth 1 -maxdepth 1 -type d -name '20??????T??????Z-*' -mmin "+$backup_retention_minutes" -exec rm -rf -- {} +
  fi
  remaining_count="$(find "$data_root/backups" -mindepth 1 -maxdepth 1 -type d -name '20??????T??????Z-*' -mmin "+$backup_retention_minutes" -print | wc -l)"
  test "$remaining_count" -eq 0
  echo "backup_retention_days=$backup_retention_days"
  echo "old_backup_directories_removed=$old_count"
}

# A first-time legacy SQLite import can take longer than the normal restart
# while the service keeps its health port closed during the transaction.
for _ in $(seq 1 150); do
  if systemctl is-active --quiet market-collector.service && systemctl is-active --quiet liquidation-predictor.service && curl -fsS http://127.0.0.1:9090/healthz >/dev/null && curl -fsS http://127.0.0.1:9090/readyz >/dev/null; then
    prune_old_backups
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
