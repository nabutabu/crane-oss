packer {
  required_plugins {
    amazon = {
      version = ">= 1.2.8"
      source  = "github.com/hashicorp/amazon"
    }
    git = {
      version = ">= 0.6.2"
      source  = "github.com/ethanmdavidson/git"
    }
  }
}

data "git-commit" "test" {}

variable "aws_access_key" {
  type        = string
  description = "AWS access key ID"
  sensitive   = true
}

variable "aws_secret_key" {
  type        = string
  description = "AWS secret access key"
  sensitive   = true
}

variable "spire_version" {
  type        = string
  default     = "1.9.1"
  description = "SPIRE version to install"
}

variable "spire_server_ip" {
  type        = string
  description = "Private IP of the SPIRE server"
}

locals {
  hash = data.git-commit.test.hash
}

source "amazon-ebs" "ubuntu" {
  ami_name      = "crane-golden-linux-aws-curl-${formatdate("YYYY-MM-DD-hhmm", timestamp())}"
  instance_type = "t4g.micro"
  region        = "us-west-2"
  access_key    = var.aws_access_key
  secret_key    = var.aws_secret_key
  source_ami_filter {
    filters = {
      name                = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-*"
      root-device-type    = "ebs"
      virtualization-type = "hvm"
    }
    most_recent = true
    owners      = ["099720109477"]
  }
  ssh_username = "ubuntu"
  tags = {
    OS_Version = "Ubuntu"
    Git_SHA    = local.hash
    TimeStamp  = formatdate("YYYY-MM-DD-hhmm", timestamp())
    Role       = "MicroWeb"
  }
}

build {
  name = "crane-golden-image-build"
  sources = [
    "source.amazon-ebs.ubuntu"
  ]
  provisioner "shell" {
    inline = ["sudo apt-get update", "sudo apt-get install -y curl"]
  }
  provisioner "file" {
    source = "${path.root}/spire-agent.conf"
    destination = "/tmp/spire-agent.conf"
  }
  provisioner "file" {
    source = "${path.root}/spire-agent.service"
    destination = "/tmp/spire-agent.service"
  }
  provisioner "shell" {
    inline = [
      "wget -q https://github.com/spiffe/spire/releases/download/v${var.spire_version}/spire-${var.spire_version}-linux-arm64-musl.tar.gz",
      "tar zvxf spire-${var.spire_version}-linux-arm64-musl.tar.gz",
      "sudo cp -r spire-${var.spire_version}/. /opt/spire/",
      "rm -rf spire-${var.spire_version}*",
      "sudo mkdir -p /opt/spire/data",
      "sudo chown root:root /opt/spire/data",
      "sudo chmod 700 /opt/spire/data",
      "sudo mkdir -p /etc/spire",
      "sudo cp /tmp/spire-agent.conf /etc/spire/agent.conf",
      "sudo cp /tmp/spire-agent.service /etc/systemd/system/spire-agent.service"
    ]
  }
  provisioner "shell" {
    inline = [
      "sudo sed -i 's/<SPIRE_SERVER_IP>/${var.spire_server_ip}/g' /etc/spire/agent.conf"
    ]
  }
  provisioner "shell" {
    script = "${path.root}/setup-k8s.sh"
  }
  provisioner "file" {
    source = "${path.root}/subd"
    destination = "/tmp/subd"
  }
  provisioner "file" {
    source = "${path.root}/subd-appsettings.json"
    destination = "/tmp/appsettings.json"
  }
  provisioner "file" {
    source = "${path.root}/subd.service"
    destination = "/tmp/subd.service"
  }
  provisioner "shell" {
    inline = [
      "sudo mv /tmp/subd /usr/local/bin/subd",
      "sudo chmod +x /usr/local/bin/subd",
      "sudo mv /tmp/appsettings.json /home/ubuntu/appsettings.json",
      "sudo mv /tmp/subd.service /etc/systemd/system/subd.service",
      "sudo systemctl enable subd"
    ]
  }
  provisioner "shell" {
    inline = [
      "sudo cp /tmp/spire-agent.conf /etc/spire/agent.conf",
      "sudo cp /tmp/spire-agent.service /etc/systemd/system/spire-agent.service",
      "sudo systemctl enable spire-agent",
      "sudo systemctl start spire-agent"
    ]
  }
}
