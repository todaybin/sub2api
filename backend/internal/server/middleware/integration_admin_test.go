//go:build unit

package middleware

import "testing"

func TestIntegrationCanonicalString(t *testing.T) {
	got := IntegrationCanonicalString(
		"POST",
		"/api/v1/admin/users/123/balance",
		"int_example",
		"1735689600",
		"nonce-123",
		[]byte(`{"balance":25,"operation":"add"}`),
	)
	want := "POST\n/api/v1/admin/users/123/balance\nint_example\n1735689600\nnonce-123\n5635ce8d80e22dbaa22a5157abd8f830d870fbd1105f8bad28c7c015bef5d235"
	if got != want {
		t.Fatalf("canonical string mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestIntegrationRouteForbidden(t *testing.T) {
	for _, path := range []string{
		"/settings/admin-api-key",
		"/settings/integration-admin/regenerate",
		"/backups/42/download",
		"/system/restart",
		"/audit-logs/clear",
	} {
		if !integrationRouteForbidden(path) {
			t.Fatalf("expected %q to be forbidden", path)
		}
	}

	for _, path := range []string{
		"/users/123/balance",
		"/users/provision",
		"/subscriptions/assign",
		"/groups",
	} {
		if integrationRouteForbidden(path) {
			t.Fatalf("expected %q to be allowed", path)
		}
	}
}
