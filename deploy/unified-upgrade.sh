#!/usr/bin/env bash
set -euo pipefail

# Root-only release procedure for the binance unified runtime. Secrets are read
# from existing systemd environment files and are never echoed or committed.
# The target host synchronizes the requested source revision and builds the
# Linux binaries locally. This keeps the release architecture-native and avoids
# uploading locally built artifacts.
app_root=/opt/Price-Prediction-Using-Liquidation-Levels
if [[ $# -ge 3 && -n "${3:-}" ]]; then
  app_root="$3"
fi
source_root="$app_root/source"
data_root=/var/lib/liquidation-predictor
legacy_root=/opt/tv_okx_bot
legacy_state=/var/lib/tv-okx-bot
revision="${1:?usage: unified-upgrade.sh <binance-revision> <build-branch> [app-root]}"
build_branch="${2:-binance}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
short_revision="${revision:0:12}"
backup="$data_root/backups/$stamp-$short_revision"
build_stage="$data_root/builds/$revision.$stamp"
release_package="$data_root/builds/liquidation-predictor-$revision-linux.tar.gz"
backup_retention_days=7
backup_retention_minutes=$((backup_retention_days * 24 * 60))
binary="$app_root/liquidation-predictor"
new_binary="$app_root/liquidation-predictor.new"
collector_binary="$app_root/market-collector"
new_collector_binary="$app_root/market-collector.new"

case "$revision" in
  (*[!0-9a-fA-F]*) echo "invalid revision" >&2; exit 1 ;;
esac
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
case "$(uname -m)" in
  x86_64) go_arch=amd64 ;;
  aarch64|arm64) go_arch=arm64 ;;
  *) echo "unsupported remote architecture: $(uname -m)" >&2; exit 1 ;;
esac
go_bin="$(command -v go || true)"
test -n "$go_bin" -a -x "$go_bin"
command -v runuser >/dev/null
command -v /usr/bin/time >/dev/null
builder=liquidation

chown -R "$builder:$builder" "$app_root"
install -d -o "$builder" -g "$builder" -m 0750 \
  "$data_root/.cache/go-build" "$data_root/go/pkg/mod" "$data_root/builds" "$build_stage"
chown -R "$builder:$builder" "$data_root/.cache/go-build" "$data_root/go/pkg/mod"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags="-s -w -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildTime=$build_time -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildRevision=$revision -X github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi.BuildBranch=$build_branch"
builder_path="$(dirname "$go_bin"):/usr/bin:/bin"
cd "$source_root"

run_builder_step() {
  local label="$1"
  shift
  echo "remote_build_step=$label"
  /usr/bin/time -f "remote_build_${label}_elapsed_seconds=%e remote_build_${label}_max_rss_kb=%M" \
    runuser -u "$builder" -- env CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" \
      GOCACHE="$data_root/.cache/go-build" GOMODCACHE="$data_root/go/pkg/mod" \
      PATH="$builder_path" "$go_bin" "$@"
}

trap 'rm -rf -- "$build_stage" "$new_binary" "$new_collector_binary"' EXIT

echo "remote_build_arch=$go_arch"
echo "remote_go_version=$("$go_bin" version)"
run_builder_step test test ./...
run_builder_step vet vet ./...
run_builder_step server-build build -buildvcs=false -trimpath "-ldflags=$ldflags" -o "$build_stage/liquidation-predictor" ./cmd/server
run_builder_step collector-build build -buildvcs=false -trimpath "-ldflags=$ldflags" -o "$build_stage/market-collector" ./cmd/market-collector
test -x "$build_stage/liquidation-predictor"
test -x "$build_stage/market-collector"

server_sha256="$(sha256sum "$build_stage/liquidation-predictor" | awk '{print $1}')"
collector_sha256="$(sha256sum "$build_stage/market-collector" | awk '{print $1}')"
manifest="$build_stage/manifest.txt"
{
  printf 'revision=%s\n' "$revision"
  printf 'branch=%s\n' "$build_branch"
  printf 'architecture=%s\n' "$go_arch"
  printf 'go_version=%s\n' "$($go_bin version)"
  printf 'build_time=%s\n' "$build_time"
  printf 'liquidation_predictor_sha256=%s\n' "$server_sha256"
  printf 'market_collector_sha256=%s\n' "$collector_sha256"
} > "$manifest"
chown "$builder:$builder" "$manifest"
tar -czf "$release_package.tmp" -C "$build_stage" liquidation-predictor market-collector manifest.txt
mv -f -- "$release_package.tmp" "$release_package"
chown "$builder:$builder" "$release_package"
chmod 0640 "$release_package"
echo "remote_release_package=$release_package"
echo "remote_release_package_sha256=$(sha256sum "$release_package" | awk '{print $1}')"
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
install -o liquidation -g liquidation -m 0755 "$build_stage/liquidation-predictor" "$new_binary"
mv -f -- "$new_binary" "$binary"
test "$(sha256sum "$binary" | awk '{print $1}')" = "$server_sha256"
echo "built_binary_sha256=$server_sha256"
install -o liquidation -g liquidation -m 0755 "$build_stage/market-collector" "$new_collector_binary"
mv -f -- "$new_collector_binary" "$collector_binary"
test "$(sha256sum "$collector_binary" | awk '{print $1}')" = "$collector_sha256"
echo "built_collector_sha256=$collector_sha256"

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
