# Usage records, for the relay's operator. Off unless var.usage_analytics.
#
# The path, end to end:
#
#   relay process -> systemd journal -> serial console   (the image does this)
#   serial console -> Cloud Logging                      (the metadata flag in main.tf)
#   Cloud Logging -> BigQuery                            (the sink below)
#
# Nothing new runs on the VM and the relay gets no new permission. Only three
# kinds of line leave Cloud Logging for BigQuery: a slot registered, a slot
# released, and a registration refused. They say who (the Google account's
# email), which tier, which CLI version and how long the slot lived. The relay
# cannot see what a machine does, so neither can these records.
#
# Queries to run against the dataset are in usage.sql.

locals {
  # BigQuery names the table after the log.
  usage_log = "projects/${var.project_id}/logs/serialconsole.googleapis.com%2Fserial_port_1_output"
}

resource "google_project_service" "bigquery" {
  count              = var.usage_analytics ? 1 : 0
  project            = var.project_id
  service            = "bigquery.googleapis.com"
  disable_on_destroy = false
}

resource "google_bigquery_dataset" "usage" {
  count       = var.usage_analytics ? 1 : 0
  project     = var.project_id
  dataset_id  = "relay_usage"
  location    = var.usage_dataset_location
  description = "Runlease relay usage records. Contains email addresses. Rows are deleted after ${var.usage_retention_days} days."
  labels      = local.labels

  # Each day's rows are deleted this long after that day.
  default_partition_expiration_ms = var.usage_retention_days * 24 * 60 * 60 * 1000

  depends_on = [google_project_service.bigquery]
}

resource "google_logging_project_sink" "usage" {
  count       = var.usage_analytics ? 1 : 0
  project     = var.project_id
  name        = "${var.name}-usage"
  description = "Runlease relay usage records to BigQuery."
  destination = "bigquery.googleapis.com/projects/${var.project_id}/datasets/${google_bigquery_dataset.usage[0].dataset_id}"

  filter = join(" AND ", [
    "resource.type=\"gce_instance\"",
    "resource.labels.instance_id=\"${google_compute_instance.relay.instance_id}\"",
    "logName=\"${local.usage_log}\"",
    "textPayload:\"runlease-relay[\"",
    "(textPayload:\"token registered\" OR textPayload:\"slot released\" OR textPayload:\"register rejected\")",
  ])

  unique_writer_identity = true

  bigquery_options {
    use_partitioned_tables = true
  }
}

# The sink writes as its own service identity, which needs to be let in.
resource "google_bigquery_dataset_iam_member" "usage_sink" {
  count      = var.usage_analytics ? 1 : 0
  project    = var.project_id
  dataset_id = google_bigquery_dataset.usage[0].dataset_id
  role       = "roles/bigquery.dataEditor"
  member     = google_logging_project_sink.usage[0].writer_identity
}
