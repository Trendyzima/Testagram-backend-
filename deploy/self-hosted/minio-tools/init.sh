#!/bin/sh
set -eu

attempt=0
until mc alias set local http://object-storage:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1 &&
      mc admin info local >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    echo "MinIO did not become ready within 120 seconds" >&2
    exit 1
  fi
  sleep 2
done

private_bucket="${MINIO_BUCKET:-testagram-private-media}"
public_bucket="${PUBLIC_MEDIA_BUCKET:-testagram-public-media}"
identity_bucket="${IDENTITY_EVIDENCE_PRIVATE_BUCKET:-testagram-identity-evidence-private}"

mc mb --ignore-existing "local/$private_bucket"
mc anonymous set none "local/$private_bucket"
mc mb --ignore-existing "local/$identity_bucket"
mc anonymous set none "local/$identity_bucket"
mc mb --ignore-existing "local/$public_bucket"
mc anonymous set download "local/$public_bucket"

if [ -n "${MINIO_MEDIA_ACCESS_KEY:-}" ] && [ -n "${MINIO_MEDIA_SECRET_KEY:-}" ]; then
  printf '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject","s3:GetObject","s3:DeleteObject"],"Resource":["arn:aws:s3:::%s/*","arn:aws:s3:::%s/*","arn:aws:s3:::%s/*"]}]}\n' \
    "$private_bucket" "$public_bucket" "$identity_bucket" > /tmp/testagram-media-policy.json
  mc admin policy create local testagram-media-api /tmp/testagram-media-policy.json || true
  mc admin user add local "$MINIO_MEDIA_ACCESS_KEY" "$MINIO_MEDIA_SECRET_KEY" >/dev/null 2>&1 || true
  mc admin policy attach local testagram-media-api --user "$MINIO_MEDIA_ACCESS_KEY"
fi

echo "MinIO buckets and restricted media identity are ready."
