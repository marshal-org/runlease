output "address" {
  description = "Point the relay hostname's A record here."
  value       = google_compute_address.relay.address
}

output "relay_addr" {
  description = "What people put in relay.addr."
  value       = "wss://${var.hostname}"
}

output "health_url" {
  value = "https://${var.hostname}/health"
}

output "service_account" {
  value = google_service_account.relay.email
}

output "usage_dataset" {
  description = "BigQuery dataset holding the usage records, when usage_analytics is on."
  value       = var.usage_analytics ? "${var.project_id}.${google_bigquery_dataset.usage[0].dataset_id}" : null
}
