package handler_test

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/media"
)

// This is a drift guard, not a full OpenAPI validator.
func TestMediaStatusContract(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Type                 json.RawMessage            `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		Enum                 []string                   `json:"enum"`
		Minimum              int                        `json:"minimum"`
		Maximum              int                        `json:"maximum"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	var spec struct {
		Security   []map[string][]string                 `json:"security"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	raw, ok := spec.Paths["/media/{assetId}/status"]["get"]
	if !ok {
		t.Fatal("missing status operation")
	}
	var op struct {
		Security   *[]map[string][]string  `json:"security"`
		Parameters []struct{ Name string } `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &op); err != nil {
		t.Fatal(err)
	}
	security := spec.Security
	if op.Security != nil {
		security = *op.Security
	}
	if len(security) != 1 {
		t.Fatal("status requires bearer authentication")
	}
	if _, ok := security[0]["ClerkSession"]; !ok {
		t.Fatalf("wrong authentication scheme: %v", security)
	}
	if len(op.Parameters) != 1 || op.Parameters[0].Name != "assetId" {
		t.Fatal("status must require only assetId, never Upload-Token")
	}
	status := spec.Components.Schemas["MediaAssetStatus"]
	typ := reflect.TypeOf(media.AssetStatus{})
	// The HTTP response adds maxAttempts; the persisted projection does not.
	if len(status.Properties) != typ.NumField()+1 || len(status.Required) != typ.NumField()+1 || status.AdditionalProperties == nil || *status.AdditionalProperties {
		t.Fatal("status shape drift")
	}
	for i := range typ.NumField() {
		key := typ.Field(i).Tag.Get("json")
		if _, ok := status.Properties[key]; !ok || !slices.Contains(status.Required, key) {
			t.Fatalf("missing required %s", key)
		}
	}
	for _, key := range []string{"failureCode", "validatedAt"} {
		var field schema
		if err := json.Unmarshal(status.Properties[key], &field); err != nil {
			t.Fatal(err)
		}
		var types []string
		if err := json.Unmarshal(field.Type, &types); err != nil || !slices.Contains(types, "null") || !slices.Contains(types, "string") {
			t.Fatalf("%s must allow explicit null", key)
		}
	}
	var attempts schema
	if err := json.Unmarshal(status.Properties["attempts"], &attempts); err != nil || attempts.Minimum != 0 || attempts.Maximum != 4 || string(attempts.Type) != `"integer"` {
		t.Fatal("attempt limits drift")
	}
	if !slices.Contains(status.Required, "maxAttempts") {
		t.Fatal("maxAttempts must be required")
	}
	var maxAttempts schema
	if err := json.Unmarshal(status.Properties["maxAttempts"], &maxAttempts); err != nil || maxAttempts.Minimum != 1 || maxAttempts.Maximum != 2 || string(maxAttempts.Type) != `"integer"` {
		t.Fatal("max attempt budget drift")
	}
	states := spec.Components.Schemas["MediaValidationState"].Enum
	want := []string{media.StatusUploadedUnvalidated, media.StatusValidating, media.StatusReady, media.StatusRejected, media.StatusValidationFailed}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("status enum drift: %v", states)
	}
}
