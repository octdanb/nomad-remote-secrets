// Packer template for a versioned Nomad client AMI (Ubuntu 24.04) with
// Docker, the multi-provider `secrets` secret provider plugin (with an
// onepassword back-compat alias), and an optional Traefik install baked in
// but disabled.
//
// The image is credential-free by design: 1Password tokens, node pool,
// datacenter, server join addresses, and Traefik enablement are all injected
// at instance launch via cloud-init user data (see examples/user-data.sh.tftpl
// and packer/README.md).
//
// Build (from the repository root):
//   make release                      # builds bin/secrets_linux_<arch>
//   cd packer
//   packer init .
//   packer build -var image_version=1.0.0 -var org_arn=arn:aws:organizations::123456789012:organization/o-abc123 .

packer {
  required_plugins {
    amazon = {
      source  = "github.com/hashicorp/amazon"
      version = ">= 1.3.0"
    }
    ansible = {
      source  = "github.com/hashicorp/ansible"
      version = ">= 1.1.0"
    }
  }
}

variable "image_version" {
  type        = string
  description = "Semantic version for this image, e.g. 1.4.0. Drives the AMI name and Version tag."

  validation {
    condition     = can(regex("^\\d+\\.\\d+\\.\\d+$", var.image_version))
    error_message = "The image_version value must be a semantic version like 1.4.0."
  }
}

variable "region" {
  type    = string
  default = "ap-southeast-2"
}

variable "arch" {
  type        = string
  default     = "amd64"
  description = "amd64 or arm64. Must match a bin/secrets_linux_<arch> built with `make release`."

  validation {
    condition     = contains(["amd64", "arm64"], var.arch)
    error_message = "The arch value must be amd64 or arm64."
  }
}

variable "nomad_version" {
  type        = string
  default     = "1.11.0"
  description = "Nomad version to install from the HashiCorp apt repository. Must be >= 1.11.0 for secret provider plugins."
}

variable "traefik_version" {
  type        = string
  default     = "3.5.2"
  description = "Traefik version baked into the image (installed but disabled by default)."
}

variable "org_arn" {
  type        = string
  default     = ""
  description = "AWS Organizations ARN (arn:aws:organizations::<mgmt-account>:organization/o-...) to share the AMI with. Empty = not shared."
}

variable "ou_arns" {
  type        = list(string)
  default     = []
  description = "Optional organizational-unit ARNs to share with instead of the whole org."
}

variable "instance_type" {
  type        = string
  default     = ""
  description = "Override the build instance type. Defaults to t3.medium (amd64) / t4g.medium (arm64)."
}

locals {
  timestamp     = formatdate("YYYYMMDDhhmm", timestamp())
  ami_name      = "nomad-client-${var.image_version}-${var.arch}-${local.timestamp}"
  instance_type = var.instance_type != "" ? var.instance_type : (var.arch == "arm64" ? "t4g.medium" : "t3.medium")
  org_arns      = var.org_arn != "" ? [var.org_arn] : []

  common_tags = {
    Name           = local.ami_name
    Version        = var.image_version
    Arch           = var.arch
    NomadVersion   = var.nomad_version
    TraefikVersion = var.traefik_version
    BaseImage      = "ubuntu-24.04"
    ManagedBy      = "packer"
    Repository     = "github.com/octdanb/nomad-secret-plugin"
  }
}

source "amazon-ebs" "nomad_client" {
  region        = var.region
  instance_type = local.instance_type
  ssh_username  = "ubuntu"
  ami_name      = local.ami_name

  source_ami_filter {
    filters = {
      name                = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-${var.arch}-server-*"
      root-device-type    = "ebs"
      virtualization-type = "hvm"
    }
    owners      = ["099720109477"] # Canonical
    most_recent = true
  }

  # Share the finished AMI with the rest of the AWS organization. NOTE:
  # sharing requires an unencrypted image or a customer-managed KMS key
  # whose policy grants the consuming accounts — see packer/README.md.
  ami_org_arns = local.org_arns
  ami_ou_arns  = var.ou_arns

  launch_block_device_mappings {
    device_name           = "/dev/sda1"
    volume_size           = 40
    volume_type           = "gp3"
    delete_on_termination = true
  }

  tags          = local.common_tags
  snapshot_tags = local.common_tags

  metadata_options {
    http_tokens = "required" # IMDSv2 only
  }
}

build {
  sources = ["source.amazon-ebs.nomad_client"]

  # All provisioning is Ansible — the same roles used on bare-metal hosts
  # (see ansible/image.yml). Requires ansible-playbook on the build machine.
  provisioner "ansible" {
    playbook_file = "../ansible/image.yml"
    use_proxy     = false
    extra_arguments = [
      "--extra-vars",
      "nomad_version=${var.nomad_version} traefik_version=${var.traefik_version} onepassword_plugin_binary=${abspath("../bin/secrets_linux_${var.arch}")}",
    ]
  }

  # Image-only finalisation (host keys, machine-id, logs) — not part of the
  # reusable roles because it must never run on a live host.
  provisioner "shell" {
    execute_command = "sudo bash '{{ .Path }}'"
    scripts         = ["scripts/cleanup.sh"]
  }

  post-processor "manifest" {
    output     = "manifest.json"
    strip_path = true
    custom_data = {
      version = var.image_version
    }
  }
}
