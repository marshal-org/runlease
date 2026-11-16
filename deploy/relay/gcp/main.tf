# A Runlease relay on one small Google Cloud VM.
#
# What it creates, all inside the project you name:
#   - a network of its own, with inbound 80 and 443 only
#   - a static address
#   - a service account with no project roles (the relay needs no cloud access)
#   - one VM that installs the relay and agent binaries, verifies their
#     SHA-256, and puts Caddy in front for TLS
#
# The relay keeps no data on disk and holds no keys. See docs/self-hosting-relay.md
# and docs/security.md.

locals {
  labels = merge({ app = "runlease-relay" }, var.labels)

  startup = templatefile("${path.module}/startup.sh.tftpl", {
    hostname             = var.hostname
    acme_email           = var.acme_email
    relay_url            = var.relay_binary_url
    relay_sha256         = var.relay_sha256
    agent_url            = var.agent_binary_url
    agent_sha256         = var.agent_sha256
    audience             = var.audience
    google_client_id     = var.google_client_id
    google_client_secret = var.google_client_secret
    allowed_emails       = join(",", var.allowed_emails)
    allowed_domains      = join(",", var.allowed_domains)
    max_slots            = var.max_slots
  })
}

resource "google_compute_network" "relay" {
  project                 = var.project_id
  name                    = "${var.name}-net"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "relay" {
  project       = var.project_id
  name          = "${var.name}-subnet"
  region        = var.region
  network       = google_compute_network.relay.id
  ip_cidr_range = "10.77.0.0/28"
}

# The only way in: HTTP (for the certificate challenge and a redirect) and HTTPS.
resource "google_compute_firewall" "web" {
  project       = var.project_id
  name          = "${var.name}-web"
  network       = google_compute_network.relay.id
  direction     = "INGRESS"
  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["runlease-relay"]

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }
}

# Shell access for the operator goes through Identity-Aware Proxy, never a
# public SSH port. Off unless asked for.
resource "google_compute_firewall" "iap_ssh" {
  count         = var.allow_iap_ssh ? 1 : 0
  project       = var.project_id
  name          = "${var.name}-iap-ssh"
  network       = google_compute_network.relay.id
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["runlease-relay"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

resource "google_compute_address" "relay" {
  project = var.project_id
  name    = "${var.name}-ip"
  region  = var.region
}

# No roles are granted to this account. A relay with no cloud access is worth
# little to whoever breaks into it, which is the point of the design.
resource "google_service_account" "relay" {
  project      = var.project_id
  account_id   = var.name
  display_name = "Runlease relay VM"
}

# Only when the binaries come from a private bucket: let the VM read them.
resource "google_storage_bucket_iam_member" "binaries" {
  count  = var.binary_bucket == "" ? 0 : 1
  bucket = var.binary_bucket
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.relay.email}"
}

resource "google_project_iam_member" "logs" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.relay.email}"
}

resource "google_compute_instance" "relay" {
  project      = var.project_id
  name         = var.name
  zone         = var.zone
  machine_type = var.machine_type
  tags         = ["runlease-relay"]
  labels       = local.labels

  # A change to the startup script replaces the VM, so what runs is always
  # what this configuration says. Slots are in memory: a replacement drops them.
  # The limits are not part of the script. They are the runlease-tiers metadata
  # value below, which changes in place: the VM copies it to the relay's tiers
  # file within a minute and the relay applies it without a restart.
  metadata_startup_script   = local.startup
  allow_stopping_for_update = true

  boot_disk {
    initialize_params {
      image = var.image
      size  = 10
      type  = "pd-standard"
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.relay.id
    access_config {
      nat_ip = google_compute_address.relay.address
    }
  }

  service_account {
    email  = google_service_account.relay.email
    scopes = ["https://www.googleapis.com/auth/devstorage.read_only", "https://www.googleapis.com/auth/logging.write"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  metadata = merge({
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
    runlease-tiers         = var.tiers_yaml
    }, var.console_to_cloud_logging ? {
    # The image already copies the journal to the serial console. This asks
    # the platform to store that console output in Cloud Logging. No agent
    # runs on the VM for it. The usage module reads the relay's records from
    # there: see usage/.
    serial-port-logging-enable = "true"
  } : {})

  lifecycle {
    precondition {
      condition     = var.audience != "" || var.google_client_id != ""
      error_message = "Set audience or google_client_id. A relay must not run without authentication."
    }
  }
}

# Optional: the DNS record, when the zone is in Google Cloud DNS.
resource "google_dns_record_set" "relay" {
  count        = var.dns_managed_zone == "" ? 0 : 1
  project      = var.dns_project == "" ? var.project_id : var.dns_project
  managed_zone = var.dns_managed_zone
  name         = "${var.hostname}."
  type         = "A"
  ttl          = 300
  rrdatas      = [google_compute_address.relay.address]
}
