variable "project_id" {
  description = "Project to create the relay in. Use one that holds nothing else."
  type        = string
}

variable "name" {
  description = "Name for the VM and the resources around it."
  type        = string
  default     = "runlease-relay"
}

variable "region" {
  type    = string
  default = "us-central1"
}

variable "zone" {
  type    = string
  default = "us-central1-a"
}

variable "machine_type" {
  description = "e2-micro is enough to start. The relay only copies bytes."
  type        = string
  default     = "e2-micro"
}

variable "image" {
  type    = string
  default = "projects/debian-cloud/global/images/family/debian-12"
}

variable "hostname" {
  description = "Public DNS name of the relay, for example relay.example.com. Its A record must point at the address this module outputs before a certificate can be issued."
  type        = string
}

variable "acme_email" {
  description = "Contact address for the TLS certificate authority."
  type        = string
}

# ── binaries ──────────────────────────────────────────────────────────────────
# Give https:// URLs (a public release) or gs:// URLs (a private bucket; also
# set binary_bucket so the VM may read it). The SHA-256 values come from the
# release's checksums.txt and are checked on the VM before anything runs.

variable "relay_binary_url" {
  description = "https:// or gs:// URL of runlease-relay-linux-amd64."
  type        = string
}

variable "relay_sha256" {
  type = string
  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.relay_sha256))
    error_message = "relay_sha256 must be 64 lowercase hex characters."
  }
}

variable "agent_binary_url" {
  description = "https:// or gs:// URL of runlease-agent-linux-amd64. Must be the agent from the same release as the CLI people use, or their VMs will refuse it."
  type        = string
}

variable "agent_sha256" {
  type = string
  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.agent_sha256))
    error_message = "agent_sha256 must be 64 lowercase hex characters."
  }
}

variable "binary_bucket" {
  description = "Name of the bucket behind gs:// URLs, so the VM can be allowed to read it. Leave empty for https:// URLs."
  type        = string
  default     = ""
}

# ── who may use it ────────────────────────────────────────────────────────────

variable "google_client_id" {
  description = "OAuth client ID (type: TVs and Limited Input devices) that 'runlease login' signs people in with."
  type        = string
  default     = ""
}

variable "google_client_secret" {
  description = "Secret of that client. The relay publishes it to CLIs for the sign-in flow; for this client type it is not confidential."
  type        = string
  default     = ""
}

variable "audience" {
  description = "The relay's public URL, to also accept identity tokens from service accounts and gcloud. Leave empty on a relay that only takes 'runlease login'."
  type        = string
  default     = ""
}

variable "allowed_emails" {
  description = "Only these accounts may open slots. Empty, together with allowed_domains empty, admits any verified Google account: the setting for a public relay."
  type        = list(string)
  default     = []
}

variable "allowed_domains" {
  type    = list(string)
  default = []
}

variable "max_slots" {
  description = "Upper bound on live slots across everyone."
  type        = number
  default     = 2000
}

variable "tiers_yaml" {
  description = "Contents of the tiers file. See deploy/relay/tiers.example.yaml. A change is applied to the running relay within about a minute, with no restart and no dropped session."
  type        = string
  default     = <<-YAML
    default: free
    tiers:
      free:
        maxSlots: 2
        slotTTL: 8h
        idleTTL: 15m
  YAML

  validation {
    condition     = can(yamldecode(var.tiers_yaml).tiers) && can(yamldecode(var.tiers_yaml).default)
    error_message = "tiers_yaml must be YAML with a `default` tier name and a `tiers` map."
  }
}

# ── operations ────────────────────────────────────────────────────────────────

variable "allow_iap_ssh" {
  description = "Allow operator SSH through Identity-Aware Proxy. There is never a public SSH port."
  type        = bool
  default     = false
}

variable "dns_managed_zone" {
  description = "Cloud DNS managed zone to create the A record in. Leave empty to manage DNS yourself."
  type        = string
  default     = ""
}

variable "dns_project" {
  description = "Project that holds the managed zone, when it is not project_id."
  type        = string
  default     = ""
}

variable "labels" {
  type    = map(string)
  default = {}
}

variable "console_to_cloud_logging" {
  description = "Store the VM's serial console output, which includes the relay's log, in Cloud Logging. Needed by the usage module (usage/). Off by default: the relay's log contains the email addresses of the people who use it."
  type        = bool
  default     = false
}
