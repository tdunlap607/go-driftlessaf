/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

// Package gcsstatusmanager provides a generic reconciliation status manager
// backed by Google Cloud Storage.
//
// Status is stored as JSON objects in GCS under the path "{identity}/{key}",
// where identity is a prefix set at construction time and key identifies the
// specific resource being reconciled.
//
// # Basic Usage
//
//	client, err := storage.NewClient(ctx)
//	if err != nil {
//		log.Fatal(err)
//	}
//	bucket := client.Bucket("my-status-bucket")
//
//	type MyDetails struct {
//		Phase string `json:"phase"`
//	}
//
//	m := gcsstatusmanager.New[MyDetails]("my-reconciler", bucket)
//
//	session, err := m.NewSession("resources/my-resource")
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	status, err := session.ObservedState(ctx)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	if err := session.SetActualState(ctx, &gcsstatusmanager.Status[MyDetails]{
//		ObservedGeneration: "abc123",
//		Details:            MyDetails{Phase: "complete"},
//	}); err != nil {
//		log.Fatal(err)
//	}
//
// # Read-Only Access
//
// Use [NewReadOnly] to create a manager that can only read status, not write it:
//
//	ro := gcsstatusmanager.NewReadOnly[MyDetails]("my-reconciler", bucket)
//
// # Thread Safety
//
// Manager and Session values are safe for concurrent use by multiple goroutines.
package gcsstatusmanager
