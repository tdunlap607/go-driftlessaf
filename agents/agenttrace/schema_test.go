/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package agenttrace

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// bqColumn is the minimum shape of a BQ schema entry — name+type are enough
// to verify that the JSON tags on RecordedSpan and the schema's columns line
// up. Mode is ignored here; the file is hand-maintained for REQUIRED markers.
type bqColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode"`
}

// TestAgentTraceSpanSchemaMatchesStruct guards against drift between the
// agent_trace_span BigQuery schema and the RecordedSpan Go struct. Any
// added/removed/renamed JSON tag must be mirrored in the schema or the BQ
// recorder will silently drop the column.
func TestAgentTraceSpanSchemaMatchesStruct(t *testing.T) {
	raw, err := os.ReadFile("iac/schemas/agent_trace_span.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var cols []bqColumn
	if err := json.Unmarshal(raw, &cols); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	gotCols := make([]string, 0, len(cols))
	for _, c := range cols {
		gotCols = append(gotCols, c.Name)
	}
	sort.Strings(gotCols)

	wantCols := jsonTags(reflect.TypeFor[RecordedSpan]())
	sort.Strings(wantCols)

	if !reflect.DeepEqual(gotCols, wantCols) {
		t.Errorf("schema columns differ from RecordedSpan JSON tags:\nschema: %v\nstruct: %v", gotCols, wantCols)
	}
}

// jsonTags returns the JSON tag names (with omitempty stripped) for all
// fields of t.
func jsonTags(t reflect.Type) []string {
	fields := reflect.VisibleFields(t)
	tags := make([]string, 0, len(fields))
	for _, field := range fields {
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		// strip ",omitempty" and friends
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			tag = tag[:comma]
		}
		tags = append(tags, tag)
	}
	return tags
}
