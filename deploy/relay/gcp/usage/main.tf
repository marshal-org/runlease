# Usage records for the relay's operator: who registered a machine, on which
# tier, with which CLI version, and how long each slot lived.
#
# The path, end to end:
#
#   relay process -> systemd journal -> serial console   (the VM image does this)
#   serial console -> Cloud Logging                      (console_to_cloud_logging on the relay module)
#   Cloud Logging -> BigQuery                            (the sink below)
#
# Nothing new runs on the VM and the relay gets no new permission. Only three
# kinds of line leave Cloud Logging for BigQuery: a slot registered, a slot
# released, and a registration refused. The relay cannot see what a machine
# does, so neither can these records.
#
# This is a module of its own so that it can be given its own provider. Some
# setups need BigQuery calls billed to a particular project (billing_project
# with user_project_override), and that setting cannot be applied to the rest
# of the relay.
#
# The BigQuery API must be enabled in the project first.
#
# Queries to run against the dataset are in usage.sql.

locals {
  usage_log = "projects/${var.project_id}/logs/serialconsole.googleapis.com%2Fserial_port_1_output"
}

resource "google_bigquery_dataset" "usage" {
  project     = var.project_id
  dataset_id  = var.dataset_id
  location    = var.location
  description = "Runlease relay usage records. Contains email addresses. Rows are deleted after ${var.retention_days} days."
  labels      = var.labels

  # Each day's rows are deleted this long after that day.
  default_partition_expiration_ms = var.retention_days * 24 * 60 * 60 * 1000
}

resource "google_logging_project_sink" "usage" {
  project     = var.project_id
  name        = "${var.name}-usage"
  description = "Runlease relay usage records to BigQuery."
  destination = "bigquery.googleapis.com/projects/${var.project_id}/datasets/${google_bigquery_dataset.usage.dataset_id}"

  filter = join(" AND ", [
    "resource.type=\"gce_instance\"",
    "resource.labels.instance_id=\"${var.instance_id}\"",
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
  project    = var.project_id
  dataset_id = google_bigquery_dataset.usage.dataset_id
  role       = "roles/bigquery.dataEditor"
  member     = google_logging_project_sink.usage.writer_identity
}

# People who may query the records. Without this only the identity that ran
# Terraform, and the project's owners, can read them.
resource "google_bigquery_dataset_iam_member" "readers" {
  for_each   = toset(var.readers)
  project    = var.project_id
  dataset_id = google_bigquery_dataset.usage.dataset_id
  role       = "roles/bigquery.dataViewer"
  member     = each.value
}
