packer {
  required_plugins {
    amazon = {
      version = ">= 1.2.8"
      source  = "github.com/hashicorp/amazon"
    }
  }
}

variable "aws_region" {
  type = string
  validation {
    condition     = can(regex("^[a-z]{2}(-[a-z]+)+-[0-9]+$", var.aws_region))
    error_message = "Provide a valid commercial AWS region."
  }
}
variable "spire_server_address" {
  type        = string
  description = "Internal NLB DNS hostname returned by crane-bootstrap -prepare-images"
  validation {
    condition     = can(regex("^[a-zA-Z0-9.-]+[.]elb[.][a-z0-9-]+[.]amazonaws[.]com$", var.spire_server_address))
    error_message = "Use the prepared private NLB hostname, never an instance IP."
  }
}
variable "spire_bundle_parameter" {
  type = string
  validation {
    condition     = can(regex("^/crane/[a-z][a-z0-9-]{2,29}/spire-bundle$", var.spire_bundle_parameter))
    error_message = "Use the deployment bundle parameter returned by -prepare-images."
  }
}
variable "trust_domain" {
  type    = string
  default = "crane.internal"
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]*[a-z0-9]$", var.trust_domain))
    error_message = "Provide a valid SPIFFE trust domain."
  }
}
variable "spire_version" {
  type    = string
  default = "1.9.3"
  validation {
    condition     = can(regex("^[0-9]+[.][0-9]+[.][0-9]+$", var.spire_version))
    error_message = "Pin an exact SPIRE release version."
  }
}
variable "kubernetes_version" {
  type    = string
  default = "v1.35.0"
  validation {
    condition     = can(regex("^v[0-9]+[.][0-9]+[.][0-9]+$", var.kubernetes_version))
    error_message = "Pin an exact Kubernetes release version."
  }
}
variable "crane_version" {
  type        = string
  description = "Release version without v; release must include bootstrap-tools and subd archives"
  validation {
    condition     = can(regex("^[0-9]+[.][0-9]+[.][0-9]+$", var.crane_version))
    error_message = "Pin an exact Crane release version without v."
  }
}
variable "architecture" {
  type    = string
  default = "amd64"
  validation {
    condition     = contains(["amd64", "arm64"], var.architecture)
    error_message = "Architecture must be amd64 or arm64."
  }
}
variable "instance_type" {
  type    = string
  default = "t3.small"
}
variable "git_sha" {
  type    = string
  default = "unknown"
}

source "amazon-ebs" "ubuntu" {
  ami_name      = "crane-golden-${var.architecture}-${formatdate("YYYY-MM-DD-hhmmss", timestamp())}"
  instance_type = var.instance_type
  region        = var.aws_region
  source_ami_filter {
    filters = {
      name                = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-${var.architecture}-server-*"
      root-device-type    = "ebs"
      virtualization-type = "hvm"
    }
    most_recent = true
    owners      = ["099720109477"]
  }
  ssh_username = "ubuntu"
  tags = {
    CraneImage           = "v2"
    Git_SHA              = var.git_sha
    CraneVersion         = var.crane_version
    AWSRegion            = var.aws_region
    SpireServerAddress   = var.spire_server_address
    SpireTrustDomain     = var.trust_domain
    SpireBundleParameter = var.spire_bundle_parameter
    SpireVersion         = var.spire_version
    KubernetesVersion    = var.kubernetes_version
  }
}

build {
  sources = ["source.amazon-ebs.ubuntu"]
  provisioner "shell" {
    script = "${path.root}/setup-host.sh"
  }
  provisioner "shell" {
    environment_vars = ["KUBERNETES_VERSION=${var.kubernetes_version}"]
    script           = "${path.root}/setup-k8s.sh"
  }
  provisioner "file" {
    content = templatefile(abspath("${path.root}/spire-agent.conf"), {
      spire_server_address = var.spire_server_address
      trust_domain         = var.trust_domain
    })
    destination = "/tmp/spire-agent.conf"
  }
  provisioner "file" {
    content = templatefile(abspath("${path.root}/spire-agent.service"), {
      aws_region             = var.aws_region
      spire_bundle_parameter = var.spire_bundle_parameter
    })
    destination = "/tmp/spire-agent.service"
  }
  provisioner "file" {
    source      = "${path.root}/fetch-spire-bundle.sh"
    destination = "/tmp/crane-fetch-spire-bundle"
  }
  provisioner "file" {
    source      = "${path.root}/configure-spire-k8s.sh"
    destination = "/tmp/crane-configure-spire-k8s"
  }
  provisioner "file" {
    source      = "${path.root}/spire-k8s-renew.service"
    destination = "/tmp/spire-k8s-renew.service"
  }
  provisioner "file" {
    source      = "${path.root}/spire-k8s-renew.timer"
    destination = "/tmp/spire-k8s-renew.timer"
  }
  provisioner "file" {
    source      = "${path.root}/subd.service"
    destination = "/tmp/subd.service"
  }
  provisioner "file" {
    source      = "${path.root}/../../policy/os.star"
    destination = "/tmp/os.star"
  }
  provisioner "shell" {
    environment_vars = [
      "SPIRE_VERSION=${var.spire_version}",
      "CRANE_VERSION=${var.crane_version}",
      "CRANE_ARCH=${var.architecture}"
    ]
    script = "${path.root}/setup-crane.sh"
  }
}
