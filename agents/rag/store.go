/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package rag

import (
	"context"
	"fmt"
	"sort"
	"time"

	aiplatform "cloud.google.com/go/aiplatform/apiv1"
	"cloud.google.com/go/aiplatform/apiv1/aiplatformpb"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/structpb"
)

// Store persists vector embeddings with metadata for later retrieval.
// Store is write-only by design; reads go through Retriever.
type Store interface {
	// Upsert inserts or updates a datapoint in the vector index.
	// The id must be non-empty and unique within the index. Metadata values
	// are stored alongside the vector for retrieval; pass UpsertOption
	// values (e.g. WithRestricts) to attach index-level restrict tags
	// usable by SearchOptions.Restricts.
	Upsert(ctx context.Context, id string, vector []float32, metadata map[string]string, opts ...UpsertOption) error

	// Close releases resources held by the store.
	Close() error
}

// Compile-time interface assertion.
var _ Store = (*MatchingEngineStore)(nil)

// MatchingEngineStore implements Store using Vertex AI Matching Engine
// with stream updates (real-time upsert, no batch import needed).
type MatchingEngineStore struct {
	client    *aiplatform.IndexClient
	indexName string // projects/{project}/locations/{location}/indexes/{index}
}

// NewMatchingEngineStore creates a store backed by Vertex AI Matching Engine.
//
// The indexName should be the full resource name:
// projects/{project}/locations/{location}/indexes/{index}
func NewMatchingEngineStore(ctx context.Context, location, indexName string) (*MatchingEngineStore, error) {
	if location == "" {
		return nil, fmt.Errorf("location is required")
	}
	if indexName == "" {
		return nil, fmt.Errorf("indexName is required")
	}

	client, err := aiplatform.NewIndexClient(ctx,
		option.WithEndpoint(fmt.Sprintf("%s-aiplatform.googleapis.com:443", location)),
	)
	if err != nil {
		return nil, fmt.Errorf("creating index client: %w", err)
	}

	return &MatchingEngineStore{
		client:    client,
		indexName: indexName,
	}, nil
}

// Upsert inserts or updates a datapoint with its embedding and metadata.
func (s *MatchingEngineStore) Upsert(ctx context.Context, id string, vector []float32, metadata map[string]string, opts ...UpsertOption) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}

	cfg := resolveUpsertOptions(opts)

	// Convert metadata to structpb for the API.
	fields := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		fields[k] = v
	}
	fields[MetadataKeyStoredAt] = time.Now().UTC().Format(time.RFC3339)

	metadataStruct, err := structpb.NewStruct(fields)
	if err != nil {
		return fmt.Errorf("creating metadata struct: %w", err)
	}

	_, err = s.client.UpsertDatapoints(ctx, &aiplatformpb.UpsertDatapointsRequest{
		Index: s.indexName,
		Datapoints: []*aiplatformpb.IndexDatapoint{{
			DatapointId:       id,
			FeatureVector:     vector,
			EmbeddingMetadata: metadataStruct,
			Restricts:         restrictsToProto(cfg.restricts),
		}},
	})
	if err != nil {
		return fmt.Errorf("upserting datapoint: %w", err)
	}
	return nil
}

// restrictsToProto converts the user-facing restrict map into the Vertex
// AI representation. Returns nil for an empty or nil input so the upsert
// request omits the field entirely (rather than sending an empty list).
func restrictsToProto(restricts map[string][]string) []*aiplatformpb.IndexDatapoint_Restriction {
	if len(restricts) == 0 {
		return nil
	}
	// Sort namespaces so the wire payload is stable across calls — easier
	// to diff in logs and avoids spurious noise in any equality testing.
	namespaces := make([]string, 0, len(restricts))
	for ns := range restricts {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	out := make([]*aiplatformpb.IndexDatapoint_Restriction, 0, len(namespaces))
	for _, ns := range namespaces {
		out = append(out, &aiplatformpb.IndexDatapoint_Restriction{
			Namespace: ns,
			AllowList: restricts[ns],
		})
	}
	return out
}

// Close releases the index client connection.
func (s *MatchingEngineStore) Close() error {
	return s.client.Close()
}
