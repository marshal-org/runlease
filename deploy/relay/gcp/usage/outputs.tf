output "dataset" {
  description = "The BigQuery dataset holding the usage records, as project.dataset."
  value       = "${var.project_id}.${google_bigquery_dataset.usage.dataset_id}"
}
