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
