package handler_test

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/krishkishore972/clip_ai/internal/preparation"
)

func TestPreparationContractMatchesPublicDTOs(t *testing.T) {
	data, err := os.ReadFile("../../openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Security   []map[string]any                      `json:"security"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required             []string                   `json:"required"`
				Properties           map[string]json.RawMessage `json:"properties"`
				AdditionalProperties bool                       `json:"additionalProperties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.Security) != 1 {
		t.Fatal("Clerk security must remain required")
	}
	if _, ok := contract.Security[0]["ClerkSession"]; !ok {
		t.Fatal("Clerk security missing")
	}
	for name, dto := range map[string]any{
		"MediaPreparationStatus": preparation.Status{},
	} {
		schema, ok := contract.Components.Schemas[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		var fields []string
		typeOf := reflect.TypeOf(dto)
		for i := 0; i < typeOf.NumField(); i++ {
			fields = append(fields, typeOf.Field(i).Tag.Get("json"))
		}
		sort.Strings(fields)
		sort.Strings(schema.Required)
		var properties []string
		for property := range schema.Properties {
			properties = append(properties, property)
		}
		sort.Strings(properties)
		if schema.AdditionalProperties || !reflect.DeepEqual(fields, schema.Required) || !reflect.DeepEqual(fields, properties) {
			t.Fatalf("%s public DTO drift: fields=%v required=%v properties=%v", name, fields, schema.Required, properties)
		}
	}
	for _, op := range []struct{ path, method, status, schema string }{
		{"/media/{assetId}/preparation", "get", "200", "MediaPreparationStatus"},
		{"/media/{assetId}/preparation", "post", "202", "MediaPreparationStatus"},
	} {
		var operation struct {
			Security    json.RawMessage `json:"security"`
			RequestBody json.RawMessage `json:"requestBody"`
			Responses   map[string]struct {
				Headers map[string]struct {
					Schema struct {
						Const string `json:"const"`
					} `json:"schema"`
				} `json:"headers"`
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(contract.Paths[op.path][op.method], &operation); err != nil {
			t.Fatal(err)
		}
		if len(operation.Security) != 0 || len(operation.RequestBody) != 0 {
			t.Fatalf("%s overrides auth or accepts request body", op.path)
		}
		response := operation.Responses[op.status]
		if response.Headers["Cache-Control"].Schema.Const != "no-store" || response.Content["application/json"].Schema.Ref != "#/components/schemas/"+op.schema {
			t.Fatalf("%s response drift", op.path)
		}
	}
}
