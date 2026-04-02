// Package registry implements the Epoch snapshot registry backed by an
// S3-compatible object store.
//
// # Integration
//
// Import this package when a controller, runtime, or CLI needs to ensure that
// Cocoon snapshots are present in the local Cocoon storage tree before use.
//
// Example:
//
//	import "github.com/cocoonstack/epoch/registry"
//
//	// Create a puller that reads object store settings from a Kubernetes ConfigMap.
//	puller, err := registry.NewPuller("/var/lib/cocoon", "prod", "agent-env")
//	if err != nil {
//	    log.WithFunc("main").Fatalf(ctx, err, "epoch puller: %v", err)
//	}
//
//	// Pre-warm known snapshots at startup (non-blocking).
//	puller.PreWarm(ctx, []string{"sre-agent-bot", "sre-agent-diagnosis"})
//
// Before creating a VM from a snapshot:
//
//	// Ensure the snapshot is present in Cocoon's local snapshot store.
//	if err := puller.EnsureSnapshot(ctx, image); err != nil {
//	    return fmt.Errorf("epoch ensure %s: %w", image, err)
//	}
//	// The caller can now resolve the local snapshot data and start the VM.
//
// The Puller is thread-safe, idempotent, and caches pull results.
// Subsequent calls for the same snapshot return immediately.
package registry
