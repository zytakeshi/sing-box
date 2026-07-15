package option

import (
	"context"
	"testing"
)

func TestConnectionFailureBackoffStrictDecode(t *testing.T) {
	// Valid config decodes with the flag set.
	var enabled Options
	if err := enabled.UnmarshalJSONContext(context.Background(),
		[]byte(`{"experimental":{"connection_failure_backoff":{"enabled":true}}}`)); err != nil {
		t.Fatalf("valid decode failed: %v", err)
	}
	if enabled.Experimental == nil ||
		enabled.Experimental.ConnectionFailureBackoff == nil ||
		!enabled.Experimental.ConnectionFailureBackoff.Enabled {
		t.Fatalf("option not decoded: %+v", enabled.Experimental)
	}

	// Unknown nested field must be rejected (strict badjson).
	var bad Options
	if err := bad.UnmarshalJSONContext(context.Background(),
		[]byte(`{"experimental":{"connection_failure_backoff":{"enabled":true,"bogus":1}}}`)); err == nil {
		t.Fatal("expected strict decode to reject an unknown nested field")
	}

	// Absent option → nil (default off, behaviorally stock).
	var absent Options
	if err := absent.UnmarshalJSONContext(context.Background(),
		[]byte(`{"experimental":{}}`)); err != nil {
		t.Fatalf("empty experimental decode failed: %v", err)
	}
	if absent.Experimental != nil && absent.Experimental.ConnectionFailureBackoff != nil {
		t.Fatal("absent option should decode to nil")
	}
}
