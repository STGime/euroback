#!/usr/bin/env bash
# Create the eurobase-platform-invoices Scaleway Object Storage
# bucket used by PR 6 of the billing stack. Idempotent — safe to
# rerun.
#
# Requires the AWS CLI (Scaleway is S3-compatible) with a profile
# preconfigured for Scaleway credentials. Example ~/.aws/config
# entry:
#
#   [profile scw]
#   region = fr-par
#   endpoint_url = https://s3.fr-par.scw.cloud
#
# Usage:
#
#   AWS_PROFILE=scw ./deploy/scripts/create-invoices-bucket.sh
#
# The bucket is created private (no ACL, no public read). The
# gateway reads via presigned URLs (5-min TTL) — see
# docs/billing/invoicing.md.

set -euo pipefail

BUCKET="${INVOICES_BUCKET:-eurobase-platform-invoices}"
ENDPOINT="${S3_ENDPOINT:-https://s3.fr-par.scw.cloud}"
REGION="${S3_REGION:-fr-par}"

echo "Creating S3 bucket: ${BUCKET} at ${ENDPOINT} (region ${REGION})"

# create-bucket is not idempotent on Scaleway; check first.
if aws s3api head-bucket --bucket "${BUCKET}" --endpoint-url "${ENDPOINT}" 2>/dev/null; then
  echo "Bucket ${BUCKET} already exists — nothing to do."
  exit 0
fi

aws s3api create-bucket \
  --bucket "${BUCKET}" \
  --endpoint-url "${ENDPOINT}" \
  --create-bucket-configuration LocationConstraint="${REGION}"

# Private ACL enforced by default on Scaleway, but re-assert
# explicitly so a Scaleway policy change doesn't silently open
# the bucket to public read.
aws s3api put-bucket-acl \
  --bucket "${BUCKET}" \
  --acl private \
  --endpoint-url "${ENDPOINT}"

echo "Bucket ${BUCKET} created (private)."
echo
echo "Next steps:"
echo "  1. Confirm the bucket appears in the Scaleway console."
echo "  2. The gateway needs the same S3 credentials it uses for"
echo "     tenant storage — no separate secret is required."
echo
echo "Bucket name is a hardcoded const in internal/billing/"
echo "invoice_render.go — the INVOICES_BUCKET env var above only"
echo "overrides the create/check step in THIS script (useful for"
echo "sandbox testing). Production always uses 'eurobase-platform-"
echo "invoices'."
