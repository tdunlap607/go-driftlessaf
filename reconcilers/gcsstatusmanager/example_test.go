/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package gcsstatusmanager_test

import (
	"context"
	"fmt"
	"log"

	"chainguard.dev/driftlessaf/reconcilers/gcsstatusmanager"
	"cloud.google.com/go/storage"
)

type reconcileDetails struct {
	Phase   string `json:"phase"`
	Message string `json:"message,omitempty"`
}

func ExampleNew() {
	ctx := context.Background()

	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	bucket := client.Bucket("my-status-bucket")

	m := gcsstatusmanager.New[reconcileDetails]("my-reconciler", bucket)

	session, err := m.NewSession("resources/my-resource")
	if err != nil {
		log.Fatal(err)
	}

	status, err := session.ObservedState(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if status == nil {
		fmt.Println("no existing status")
	}
}

func ExampleNewReadOnly() {
	ctx := context.Background()

	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	bucket := client.Bucket("my-status-bucket")

	ro := gcsstatusmanager.NewReadOnly[reconcileDetails]("my-reconciler", bucket)

	session, err := ro.NewSession("resources/my-resource")
	if err != nil {
		log.Fatal(err)
	}

	status, err := session.ObservedState(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if status != nil {
		fmt.Println("phase:", status.Details.Phase)
	}
}

func ExampleSession_SetActualState() {
	ctx := context.Background()

	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	bucket := client.Bucket("my-status-bucket")

	m := gcsstatusmanager.New[reconcileDetails]("my-reconciler", bucket)

	session, err := m.NewSession("resources/my-resource")
	if err != nil {
		log.Fatal(err)
	}

	if err := session.SetActualState(ctx, &gcsstatusmanager.Status[reconcileDetails]{
		ObservedGeneration: "abc123",
		Details: reconcileDetails{
			Phase:   "complete",
			Message: "reconciliation succeeded",
		},
	}); err != nil {
		log.Fatal(err)
	}
}

func ExampleSession_ObservedState() {
	ctx := context.Background()

	client, err := storage.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	bucket := client.Bucket("my-status-bucket")

	m := gcsstatusmanager.New[reconcileDetails]("my-reconciler", bucket)

	session, err := m.NewSession("resources/my-resource")
	if err != nil {
		log.Fatal(err)
	}

	status, err := session.ObservedState(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if status == nil {
		fmt.Println("no status recorded yet")
		return
	}
	fmt.Println("generation:", status.ObservedGeneration)
}
