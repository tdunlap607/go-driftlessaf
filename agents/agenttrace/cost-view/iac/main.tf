/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

// agent_trace_costs is a view over the agent trace table that adds per-row USD
// cost estimates derived from the model and token counts. Pricing is sourced
// from the Vertex AI generative AI pricing page (Global region) and matches the
// provider used by driftlessaf at public/go-driftlessaf/agents/metaagent/claude.go.
//
// The view is written into a dataset supplied by the caller so multiple
// derived views can share one dataset and the recorder dataset stays reserved
// for raw event ingest. The view's location is implied by view_dataset_id and
// must match source_dataset_id.
//
// Models recognised today: Claude Sonnet/Opus/Haiku 4.5+, Claude Opus 4.7,
// Gemini 2.0/2.5/3.x families. Unknown models produce NULL costs (visible
// signal rather than silent zero) so that drift in model usage is noticeable.
resource "google_bigquery_table" "agent_trace_costs" {
  project    = var.project_id
  dataset_id = var.view_dataset_id
  table_id   = var.view_table_id

  friendly_name = "Agent traces with cost estimates"
  description   = "Agent traces enriched with USD cost estimates derived from model and token counts. Vertex AI Global pricing; cache writes use 5m TTL rate."

  deletion_protection = var.deletion_protection

  view {
    query = templatefile("${path.module}/sql/agent_trace_costs.sql", {
      project_id      = var.project_id
      dataset_id      = var.source_dataset_id
      source_table_id = var.source_table_id
    })
    use_legacy_sql = false
  }
}
