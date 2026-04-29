/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

variable "project_id" {
  description = "GCP project that owns both datasets."
  type        = string
}

variable "source_dataset_id" {
  description = "BigQuery dataset id holding the raw agent trace events (the dataset_id output of cloudevent-recorder). The view reads from this dataset."
  type        = string
}

variable "source_table_id" {
  description = "BigQuery table id for the agent trace events within source_dataset_id. Defaults to the table created from the recorder-schemas key dev.chainguard.driftlessaf.agent.trace.v1."
  type        = string
  default     = "dev_chainguard_driftlessaf_agent_trace_v1"
}

variable "view_dataset_id" {
  description = "BigQuery dataset id for the derived view. The caller is expected to provision this dataset (typically a shared 'agent_trace_views' dataset that hosts other derived views too) and pass its id here. Must be in the same location as source_dataset_id."
  type        = string
}

variable "view_table_id" {
  description = "BigQuery view id created by this module within view_dataset_id."
  type        = string
  default     = "dev_chainguard_driftlessaf_agent_trace_v1_with_costs"
}

variable "deletion_protection" {
  description = "Whether the view should be protected from deletion."
  type        = bool
  default     = false
}
