#!/bin/bash

################################################################################
# Kubernetes Installation Script for AWS EC2
# This script installs Docker, Kind, and Kubernetes (kubeadm, kubelet, kubectl) on Ubuntu/Debian
# Tested on Ubuntu 20.04/22.04
# After running this script, you can:
#   - Run "kind create cluster" to create a local Kubernetes cluster
#   - Run "kubectl" commands to interact with the cluster
################################################################################

set -e  # Exit on any error

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}Starting Kubernetes installation...${NC}"

# Update system packages
echo -e "${YELLOW}[1/10] Updating system packages...${NC}"
sudo apt-get update
sudo apt-get upgrade -y

# Disable swap (required for Kubernetes)
echo -e "${YELLOW}[2/10] Disabling swap...${NC}"
sudo swapoff -a
sudo sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab

# Load required kernel modules
echo -e "${YELLOW}[3/10] Loading kernel modules...${NC}"
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF

sudo modprobe overlay
sudo modprobe br_netfilter

# Configure sysctl parameters
echo -e "${YELLOW}[4/10] Configuring sysctl parameters...${NC}"
cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

sudo sysctl --system

# Install containerd and Docker
echo -e "${YELLOW}[5/10] Installing containerd and Docker...${NC}"
sudo apt-get install -y apt-transport-https ca-certificates curl software-properties-common containerd docker.io

# Configure containerd
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml > /dev/null
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

sudo systemctl restart containerd
sudo systemctl enable containerd

# Start and enable Docker
sudo systemctl start docker
sudo systemctl enable docker
sudo usermod -aG docker $USER || true

# Install Kind
echo -e "${YELLOW}[6/10] Installing Kind...${NC}"
if ! command -v kind &> /dev/null; then
    KIND_VERSION=$(curl -s https://api.github.com/repos/kubernetes-sigs/kind/releases/latest | grep -oP '"tag_name": "\K[^"]+')
    
    # Detect system architecture
    ARCH=$(uname -m)
    case $ARCH in
        x86_64) KIND_ARCH="amd64" ;;
        aarch64|arm64) KIND_ARCH="arm64" ;;
        *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
    esac
    
    curl -Lo /tmp/kind "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${KIND_ARCH}"
    sudo chmod +x /tmp/kind
    sudo mv /tmp/kind /usr/local/bin/kind
fi

# Verify Docker and Kind installations
echo -e "${YELLOW}[7/10] Verifying installations...${NC}"
docker --version
kind version

# Install Kubernetes components
echo -e "${YELLOW}[8/10] Installing Kubernetes components...${NC}"

# Add Kubernetes GPG key and repository
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.28/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.28/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list

sudo apt-get update
sudo apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl

# Enable kubelet service
sudo systemctl enable kubelet

# Initialize Kubernetes cluster (optional - uncomment to auto-initialize)
echo -e "${YELLOW}[9/10] Kubernetes components installed successfully!${NC}"
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
echo -e "${GREEN}For Kind clusters, you can now run:${NC}"
echo -e "  kind create cluster"
echo ""
echo -e "${YELLOW}[10/10] Installation complete!${NC}"

# Create default Kind cluster if it doesn't exist
echo -e "${YELLOW}Creating default Kind cluster (if not exists)...${NC}"
if ! sudo kind get clusters 2>/dev/null | grep -q "^kind$"; then
    sudo kind create cluster
    echo -e "${GREEN}Default Kind cluster created successfully!${NC}"
else
    echo -e "${GREEN}Kind cluster already exists, skipping creation.${NC}"
fi

# Export kubeconfig for ubuntu user
sudo kind export kubeconfig --name kind

# Copy kubeconfig to ubuntu user's home and set permissions
if [ -f /root/.kube/config ]; then
    mkdir -p /home/ubuntu/.kube
    cp /root/.kube/config /home/ubuntu/.kube/config
    chown -R ubuntu:ubuntu /home/ubuntu/.kube
    chmod 600 /home/ubuntu/.kube/config
    echo -e "${GREEN}Kubeconfig copied to /home/ubuntu/.kube/config${NC}"
fi

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