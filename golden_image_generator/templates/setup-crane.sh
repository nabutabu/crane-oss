#!/bin/bash
# Install release artifacts in the AMI. Never start agents or initialize their
# key/data directories on the temporary Packer instance.
set -euo pipefail
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
cd "$stage"
base="https://github.com/nabutabu/crane-oss/releases/download/v${CRANE_VERSION}"
for archive in "subd_${CRANE_VERSION}_linux_${CRANE_ARCH}.tar.gz" "bootstrap-tools_${CRANE_VERSION}_linux_${CRANE_ARCH}.tar.gz"; do
  curl -fsSL "$base/$archive" -o "$archive"
done
curl -fsSL "$base/checksums.txt" -o checksums.txt
for archive in "subd_${CRANE_VERSION}_linux_${CRANE_ARCH}.tar.gz" "bootstrap-tools_${CRANE_VERSION}_linux_${CRANE_ARCH}.tar.gz"; do
  grep -F "  $archive" checksums.txt | sha256sum -c -
  tar -xzf "$archive"
done
sudo install -m 0755 subd /usr/local/bin/subd
sudo install -m 0755 crane-bootstrap crane-bootstrap-probe /opt/crane/bin/
sudo install -d -m 0755 /opt/crane/policy
sudo install -m 0644 /tmp/os.star /opt/crane/policy/os.star

curl -fsSL "https://github.com/spiffe/spire/releases/download/v${SPIRE_VERSION}/spire-${SPIRE_VERSION}-linux-${CRANE_ARCH}-musl.tar.gz" -o spire.tar.gz
tar -xzf spire.tar.gz
sudo install -m 0755 "spire-${SPIRE_VERSION}/bin/spire-agent" /usr/local/bin/spire-agent
sudo install -m 0644 /tmp/spire-agent.conf /etc/spire/agent.conf
sudo install -m 0644 /tmp/spire-agent.conf /etc/spire/agent-base.conf
sudo install -m 0644 /tmp/spire-agent.service /etc/systemd/system/spire-agent.service
sudo install -m 0755 /tmp/crane-configure-spire-k8s /usr/local/bin/crane-configure-spire-k8s
sudo install -m 0644 /tmp/spire-k8s-renew.service /etc/systemd/system/crane-spire-k8s-renew.service
sudo install -m 0644 /tmp/spire-k8s-renew.timer /etc/systemd/system/crane-spire-k8s-renew.timer
sudo install -m 0755 /tmp/crane-fetch-spire-bundle /usr/local/bin/crane-fetch-spire-bundle
sudo install -m 0644 /tmp/subd.service /etc/systemd/system/subd.service
sudo systemctl daemon-reload
sudo systemctl enable spire-agent subd
# appsettings.json is supplied on deployment; a generic AMI has no host ID,
# SPIRE key, bundle, SVID, or initialized Kubernetes/Kind cluster. Subd accepts
# private mTLS requests; deployment config contains no host token.
