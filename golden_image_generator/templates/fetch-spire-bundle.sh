#!/bin/bash
# AWS TLS and the instance role authenticate the initial public trust bundle.
# SPIRE then authenticates the server through this bundle and maintains its
# live bundle through the SPIRE protocol.
set -euo pipefail
umask 077
: "${AWS_REGION:?AWS_REGION is required}"
: "${SPIRE_BUNDLE_PARAMETER:?SPIRE_BUNDLE_PARAMETER is required}"
install -d -m 0755 /etc/crane
bundle="$(mktemp /etc/crane/.bundle-XXXXXX)"
trap 'rm -f "$bundle"' EXIT
aws ssm get-parameter --region "$AWS_REGION" --name "$SPIRE_BUNDLE_PARAMETER" \
  --query Parameter.Value --output text > "$bundle"
openssl x509 -in "$bundle" -noout >/dev/null
chmod 0644 "$bundle"
mv -f "$bundle" /etc/crane/bundle.pem
