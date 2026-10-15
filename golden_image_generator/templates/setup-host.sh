#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
sudo cloud-init status --wait >/dev/null
sudo apt-get update
sudo apt-get install -y ca-certificates curl unzip jq python3 postgresql-client docker.io openssl util-linux
sudo install -d -m 0755 /etc/crane /opt/crane/bin /var/lib/crane /etc/spire
curl -fsSL https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem | sudo tee /etc/crane/rds-ca.pem >/dev/null
case "$(uname -m)" in
  x86_64) AWS_ARCH=x86_64 ;;
  aarch64) AWS_ARCH=aarch64 ;;
  *) exit 1 ;;
esac
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${AWS_ARCH}.zip" -o "$stage/aws.zip"
unzip -q "$stage/aws.zip" -d "$stage"
sudo "$stage/aws/install" --update
if ! snap list amazon-ssm-agent >/dev/null 2>&1; then sudo snap install amazon-ssm-agent --classic; fi
sudo snap start amazon-ssm-agent
