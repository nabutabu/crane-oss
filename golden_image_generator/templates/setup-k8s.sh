#!/bin/bash

################################################################################
# Kubernetes Installation Script for AWS EC2
# This script installs Kubernetes (kubeadm, kubelet, kubectl) on Ubuntu/Debian
# Tested on Ubuntu 20.04/22.04
################################################################################

set -e  # Exit on any error

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}Starting Kubernetes installation...${NC}"

# Update system packages
echo -e "${YELLOW}[1/8] Updating system packages...${NC}"
sudo apt-get update
sudo apt-get upgrade -y

# Disable swap (required for Kubernetes)
echo -e "${YELLOW}[2/8] Disabling swap...${NC}"
sudo swapoff -a
sudo sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab

# Load required kernel modules
echo -e "${YELLOW}[3/8] Loading kernel modules...${NC}"
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF

sudo modprobe overlay
sudo modprobe br_netfilter

# Configure sysctl parameters
echo -e "${YELLOW}[4/8] Configuring sysctl parameters...${NC}"
cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

sudo sysctl --system

# Install containerd
echo -e "${YELLOW}[5/8] Installing containerd...${NC}"
sudo apt-get install -y apt-transport-https ca-certificates curl software-properties-common

# Add Docker's official GPG key and repository (for containerd)
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

sudo apt-get update
sudo apt-get install -y containerd.io

# Configure containerd
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml > /dev/null
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

sudo systemctl restart containerd
sudo systemctl enable containerd

# Install Kubernetes components
echo -e "${YELLOW}[6/8] Installing Kubernetes components...${NC}"

# Add Kubernetes GPG key and repository
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.28/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.28/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list

sudo apt-get update
sudo apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl

# Enable kubelet service
sudo systemctl enable kubelet

# Initialize Kubernetes cluster (optional - uncomment to auto-initialize)
echo -e "${YELLOW}[7/8] Kubernetes components installed successfully!${NC}"
echo -e "${GREEN}To initialize the cluster, run:${NC}"
echo -e "  sudo kubeadm init --pod-network-cidr=10.244.0.0/16"
echo ""
echo -e "${GREEN}After initialization, configure kubectl:${NC}"
echo -e "  mkdir -p \$HOME/.kube"
echo -e "  sudo cp -i /etc/kubernetes/admin.conf \$HOME/.kube/config"
echo -e "  sudo chown \$(id -u):\$(id -g) \$HOME/.kube/config"
echo ""
echo -e "${GREEN}Then install a CNI plugin (e.g., Flannel):${NC}"
echo -e "  kubectl apply -f https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml"
echo ""
echo -e "${YELLOW}[8/8] Installation complete!${NC}"

# Optional: Uncomment the following lines to auto-initialize the cluster
# echo -e "${YELLOW}Initializing Kubernetes cluster...${NC}"
# sudo kubeadm init --pod-network-cidr=10.244.0.0/16
#
# mkdir -p $HOME/.kube
# sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config
# sudo chown $(id -u):$(id -g) $HOME/.kube/config
#
# echo -e "${GREEN}Installing Flannel CNI...${NC}"
# kubectl apply -f https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml
#
# echo -e "${GREEN}Cluster initialization complete!${NC}"