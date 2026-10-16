#!/bin/bash
# Shared image-time Kubernetes prerequisites. Cluster identity is initialized
# on the deployed host, never cloned from the Packer build instance.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
KUBERNETES_VERSION="${KUBERNETES_VERSION:-v1.35.0}"
K8S_MINOR="${KUBERNETES_VERSION%.*}"
K8S_DEB_PREFIX="${KUBERNETES_VERSION#v}-"
HELM_VERSION="v3.16.4"

sudo swapoff -a
sudo sed -i '/\sswap\s/s/^/#/' /etc/fstab
printf '%s\n' overlay br_netfilter | sudo tee /etc/modules-load.d/k8s.conf >/dev/null
sudo modprobe overlay
sudo modprobe br_netfilter
printf '%s\n' 'net.bridge.bridge-nf-call-iptables = 1' 'net.bridge.bridge-nf-call-ip6tables = 1' 'net.ipv4.ip_forward = 1' | sudo tee /etc/sysctl.d/k8s.conf >/dev/null
sudo sysctl --system >/dev/null
sudo apt-get update
sudo apt-get install -y ca-certificates curl gpg containerd docker.io
sudo mkdir -p /etc/containerd /etc/apt/keyrings
containerd config default | sudo tee /etc/containerd/config.toml >/dev/null
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
sudo systemctl enable --now containerd docker
sudo systemctl restart containerd
curl -fsSL "https://pkgs.k8s.io/core:/stable:/${K8S_MINOR}/deb/Release.key" | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
printf '%s\n' "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/${K8S_MINOR}/deb/ /" | sudo tee /etc/apt/sources.list.d/kubernetes.list >/dev/null
sudo apt-get update
K8S_DEB_VERSION="$(apt-cache madison kubeadm | awk -F'|' '{gsub(/ /,"",$2); print $2}' | grep -F "$K8S_DEB_PREFIX" | sort -V | tail -n1)"
test -n "$K8S_DEB_VERSION"
sudo apt-get install -y "kubeadm=$K8S_DEB_VERSION" "kubelet=$K8S_DEB_VERSION" "kubectl=$K8S_DEB_VERSION"
sudo apt-mark hold kubeadm kubelet kubectl
sudo systemctl enable kubelet

case "$(uname -m)" in
  x86_64) IMAGE_ARCH=amd64 ;;
  aarch64) IMAGE_ARCH=arm64 ;;
  *) exit 1 ;;
esac
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${IMAGE_ARCH}.tar.gz" -o "$stage/helm.tar.gz"
tar -xzf "$stage/helm.tar.gz" -C "$stage"
sudo install -m 0755 "$stage/linux-$IMAGE_ARCH/helm" /usr/local/bin/helm
# Do not run kind create cluster or kubeadm init while baking an AMI.
