#!/usr/bin/env bash
# Floci ready-hook: provisions the S3 document bucket document refs store their
# content in (docref/s3content), configured as docs/runbooks/document-refs.md
# requires. Runs automatically on container start via the volume mount to
# /etc/floci/init/ready.d/.
#
#   S3:
#   - workflow-connectors-documents: document content for refs, under the
#     docrefs/ prefix, with a lifecycle rule expiring that prefix after 2 days
#     (ref TTL 24 h + margin). Public access blocked.
#
# Integration tests create their own buckets; this one is for local worker
# development (s3content.New(client, "workflow-connectors-documents") with
# docref.WithKeyPrefix("docrefs/")).
set -euo pipefail

# The AWS CLI in the floci compat image defaults its region independently of
# FLOCI_DEFAULT_REGION; pin it so every call lands in the same region.
export AWS_REGION=us-east-1
export AWS_DEFAULT_REGION=us-east-1

BUCKET=workflow-connectors-documents

aws s3api create-bucket --bucket "$BUCKET" >/dev/null 2>&1 || true
aws s3api put-public-access-block --bucket "$BUCKET" --public-access-block-configuration \
  BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true || true
aws s3api put-bucket-lifecycle-configuration --bucket "$BUCKET" --lifecycle-configuration '{
  "Rules": [{
    "ID": "expire-document-refs",
    "Status": "Enabled",
    "Filter": {"Prefix": "docrefs/"},
    "Expiration": {"Days": 2}
  }]
}'
echo "init-floci: bucket $BUCKET ready"
