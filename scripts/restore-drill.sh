#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-ap-southeast-2}"
drill_dir="${DRILL_DIR:-/var/backups/recall-drill}"
aws_cli_image="amazon/aws-cli:2.36.44"
compose=(docker compose -f compose.yaml -f compose.prod.yaml)
drill_db="recall_restore_drill"

psql() {
  "${compose[@]}" exec -T postgres psql -U recall -v ON_ERROR_STOP=1 -qAt "$@"
}

aws() {
  docker run --rm \
    -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION \
    -v "$drill_dir:/drill" \
    "$aws_cli_image" "$@"
}

mkdir -p "$drill_dir"
drill_started=$(date +%s)

if [ "$#" -ge 1 ]; then
  dump="$1"
  echo "using local dump: $dump"
  download_seconds=0
else
  : "${BACKUP_BUCKET:?BACKUP_BUCKET must be set}"
  latest=$(aws s3 ls "s3://$BACKUP_BUCKET/" | awk '{print $4}' | grep '^recall-.*\.dump$' | sort | tail -n 1)
  [ -n "$latest" ] || { echo "no backups found in s3://$BACKUP_BUCKET/"; exit 1; }
  echo "latest backup in S3: $latest"
  started=$(date +%s)
  aws s3 cp --only-show-errors "s3://$BACKUP_BUCKET/$latest" "/drill/$latest"
  download_seconds=$(( $(date +%s) - started ))
  dump="$drill_dir/$latest"
fi

psql -d postgres -c "DROP DATABASE IF EXISTS $drill_db"
psql -d postgres -c "CREATE DATABASE $drill_db"

started=$(date +%s)
"${compose[@]}" exec -T postgres pg_restore -U recall -d "$drill_db" --exit-on-error < "$dump"
restore_seconds=$(( $(date +%s) - started ))

echo
printf "%-20s %10s %10s\n" "table" "live" "restored"
mismatches=0
for table in $(psql -d "$drill_db" -c "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename"); do
  live=$(psql -d recall -c "SELECT count(*) FROM $table")
  restored=$(psql -d "$drill_db" -c "SELECT count(*) FROM $table")
  marker=""
  [ "$live" = "$restored" ] || { marker="  <- differs"; mismatches=$((mismatches + 1)); }
  printf "%-20s %10s %10s%s\n" "$table" "$live" "$restored" "$marker"
done

psql -d postgres -c "DROP DATABASE $drill_db"
[ "$#" -ge 1 ] || rm -f "$dump"

echo
echo "download: ${download_seconds}s   restore: ${restore_seconds}s   whole drill: $(( $(date +%s) - drill_started ))s   tables differing: $mismatches"
[ "$mismatches" -eq 0 ]
