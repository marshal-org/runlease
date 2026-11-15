variable "project_id" {
  description = "The project the relay runs in."
  type        = string
}

variable "name" {
  description = "The relay's name, as given to the relay module. Names the log sink."
  type        = string
  default     = "runlease-relay"
}

variable "instance_id" {
  description = "The relay VM's numeric ID: the relay module's instance_id output."
  type        = string
}

variable "dataset_id" {
  description = "BigQuery dataset to create for the records."
  type        = string
  default     = "relay_usage"
}

variable "location" {
  description = "Where the BigQuery dataset lives."
  type        = string
  default     = "US"
}

variable "retention_days" {
  description = "Days a usage record is kept before it is deleted. Say the same number in your privacy notice."
  type        = number
  default     = 90
}

variable "labels" {
  description = "Labels for the dataset."
  type        = map(string)
  default     = {}
}

variable "readers" {
  description = "Who may read the records, as IAM members (for example \"user:ada@example.com\" or \"group:analytics@example.com\"). They also need permission to run queries in some project: roles/bigquery.jobUser."
  type        = list(string)
  default     = []
}
