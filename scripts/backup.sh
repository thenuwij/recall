#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

: "${BACKUP_BUCKET:?BACKUP_BUCKET must be set}"
: "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID must be set}"
: "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY must be set}"
export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-ap-southeast-2}"

backup_dir="${BACKUP_DIR:-/var/backups/recall}"
keep_local="${BACKUP_KEEP_LOCAL:-3}"
aws_cli_image="amazon/aws-cli:2.36.44"
compose=(docker compose -f compose.yaml -f compose.prod.yaml)

name="recall-$(date -u +%Y-%m-%dT%H-%M-%SZ).dump"
started=$(date +%s)

mkdir -p "$backup_dir"
"${compose[@]}" exec -T postgres pg_dump -U recall -d recall --format=custom > "$backup_dir/$name.partial"
"${compose[@]}" exec -T postgres pg_restore --list < "$backup_dir/$name.partial" > /dev/null
mv "$backup_dir/$name.partial" "$backup_dir/$name"

docker run --rm \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_DEFAULT_REGION \
  -v "$backup_dir:/backups:ro" \
  "$aws_cli_image" s3 cp --only-show-errors "/backups/$name" "s3://$BACKUP_BUCKET/$name"

ls -1t "$backup_dir"/recall-*.dump | tail -n +"$((keep_local + 1))" | xargs -r rm --

size=$(du -h "$backup_dir/$name" | cut -f1)
echo "backup uploaded: s3://$BACKUP_BUCKET/$name ($size, $(( $(date +%s) - started ))s)"
