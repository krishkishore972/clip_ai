package handler

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// JSON is valid YAML. Keeping this OpenAPI document JSON-compatible lets these
// small contract checks run with the standard library, without a YAML dependency.
// This checks drift, not full OpenAPI/schema conformance or client generation.
func TestUploadContract(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI    string                                `json:"openapi"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties           map[string]json.RawMessage `json:"properties"`
				Required             []string                   `json:"required"`
				AdditionalProperties *bool                      `json:"additionalProperties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("unexpected OpenAPI version: %s", spec.OpenAPI)
	}
	for path, methods := range map[string][]string{
		"/uploads":                       {"post"},
		"/uploads/{uploadId}":            {"get", "delete"},
		"/uploads/{uploadId}/parts/sign": {"post"},
		"/uploads/{uploadId}/complete":   {"post"},
	} {
		for _, method := range methods {
			if _, ok := spec.Paths[path][method]; !ok {
				t.Errorf("missing contract operation %s %s", method, path)
			}
		}
	}
	for _, value := range []any{
		CreateUploadRequest{}, SignUploadPartsRequest{}, CompleteUploadRequest{}, CompleteUploadPart{},
		UploadSession{}, UploadedPart{}, SignedUploadPart{}, SignedUploadParts{},
	} {
		typ := reflect.TypeOf(value)
		schema, ok := spec.Components.Schemas[typ.Name()]
		if !ok {
			t.Errorf("missing schema %s", typ.Name())
			continue
		}
		if len(schema.Properties) != typ.NumField() || len(schema.Required) != typ.NumField() {
			t.Errorf("%s: contract field count does not match Go type", typ.Name())
		}
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Tag.Get("json")
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("%s.%s missing from contract", typ.Name(), name)
			}
		}
		if strings.HasSuffix(typ.Name(), "Request") || typ.Name() == "CompleteUploadPart" {
			if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
				t.Errorf("%s must reject unknown fields", typ.Name())
			}
		}
	}
	// Resolve every local reference to catch dangling schemas and responses.
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				var target any = document
				for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					object, ok := target.(map[string]any)
					if !ok {
						t.Fatalf("invalid reference: %s", ref)
					}
					target, ok = object[segment]
					if !ok {
						t.Fatalf("missing reference: %s", ref)
					}
				}
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(document)
}
