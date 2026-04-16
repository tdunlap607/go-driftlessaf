/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

// recorder-schemas exposes the BigQuery schema for agent trace CloudEvents.
// Pass this output to the cloudevent-recorder module's types variable to
// archive traces from a driftlessaf agent to BigQuery.
//
// Usage:
//
//   module "agenttrace" {
//     source = "path/to/public/go-driftlessaf/agents/agenttrace/iac"
//   }
//
//   module "trace-recorder" {
//     source = "path/to/public/terraform-infra-common/modules/cloudevent-recorder"
//     ...
//     types = module.agenttrace.recorder-schemas
//   }
output "recorder-schemas" {
  value = {
    "dev.chainguard.driftlessaf.agent.trace.v1" = {
      schema          = file("${path.module}/schemas/agent_trace.schema.json")
      partition_field = "start_time"
    }
  }
}
