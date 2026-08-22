package dynq

import (
	"encoding/json"
	"testing"
)

// ---- @api:mode 推导 ----

func TestInferAPIMode(t *testing.T) {
	base := FieldMeta{API: &FieldAPI{Endpoint: "/x", ValueKey: "id", LabelKey: "name"}}

	// ops 推导:in → multi;fuzzy → search;否则 single。
	if got := inferAPIMode(FieldMeta{API: base.API, Ops: []Op{OpIn, OpEq}}); got != "multi" {
		t.Fatalf("in → multi, got %s", got)
	}
	if got := inferAPIMode(FieldMeta{API: base.API, Ops: []Op{OpContains, OpEq}}); got != "search" {
		t.Fatalf("fuzzy → search, got %s", got)
	}
	if got := inferAPIMode(FieldMeta{API: base.API, Ops: []Op{OpPrefix}}); got != "search" {
		t.Fatalf("prefix → search, got %s", got)
	}
	if got := inferAPIMode(FieldMeta{API: base.API, Ops: []Op{OpEq}}); got != "single" {
		t.Fatalf("eq → single, got %s", got)
	}
	if got := inferAPIMode(FieldMeta{API: base.API}); got != "single" {
		t.Fatalf("no ops → single, got %s", got)
	}

	// 显式声明覆盖推导(eq 也想搜索)。
	explicit := &FieldAPI{Endpoint: "/x", ValueKey: "id", LabelKey: "name", Mode: "search"}
	if got := inferAPIMode(FieldMeta{API: explicit, Ops: []Op{OpIn}}); got != "search" {
		t.Fatalf("declared mode wins, got %s", got)
	}
}

// ---- @api:schema 输出 ----

func TestBuildSchemaAPI(t *testing.T) {
	meta := TableMeta{
		Resource: "orders",
		Fields: []FieldMeta{
			{Key: "id", Column: "id", DataType: DataTypeInteger, Sortable: true},
			{Key: "userId", Column: "user_id", DataType: DataTypeInteger, Selectable: true,
				Ops: []Op{OpEq, OpIn},
				API: &FieldAPI{Endpoint: "/api/v1/users/options", ValueKey: "id", LabelKey: "name"}},
			// 显式 mode 声明。
			{Key: "ownerId", Column: "owner_id", DataType: DataTypeUUID, Selectable: true,
				Ops: []Op{OpEq},
				API: &FieldAPI{Endpoint: "/api/v1/owners", ValueKey: "id", LabelKey: "name", Mode: "search"}},
			// @api 与 @hidden 互斥(生成期 fail),此处仅验证 hidden 列不输出。
			{Key: "secret", Column: "secret", DataType: DataTypeString, Hidden: true,
				API: &FieldAPI{Endpoint: "/x", ValueKey: "v", LabelKey: "l"}},
		},
		Tiebreaker:  "id",
		PageDefault: 20,
		PageMax:     100,
	}
	s := BuildSchema(meta, nil)

	var userAPI, ownerAPI *FieldAPI
	for i := range s.Fields {
		switch s.Fields[i].Field {
		case "userId":
			userAPI = s.Fields[i].API
		case "ownerId":
			ownerAPI = s.Fields[i].API
		case "secret":
			t.Fatal("hidden field must not appear in schema")
		case "id":
			if s.Fields[i].API != nil {
				t.Fatal("field without @api must have nil api")
			}
		}
	}
	if userAPI == nil {
		t.Fatal("userId must carry api")
	}
	if userAPI.Endpoint != "/api/v1/users/options" || userAPI.ValueKey != "id" || userAPI.LabelKey != "name" {
		t.Fatalf("api spec: %+v", userAPI)
	}
	if userAPI.Mode != "multi" { // ops 含 in → 推导 multi
		t.Fatalf("userId mode: %s", userAPI.Mode)
	}
	if ownerAPI == nil || ownerAPI.Mode != "search" { // 显式声明优先
		t.Fatalf("ownerId mode: %+v", ownerAPI)
	}

	// 幂等:同 opts 产出相同 ETag。
	if s.Hash() != BuildSchema(meta, nil).Hash() {
		t.Fatal("schema hash must be deterministic")
	}
	// JSON 字段名契约。
	got := mustJSONFieldNames(t, s)
	for _, want := range []string{`"api"`, `"endpoint"`, `"valueKey"`, `"labelKey"`, `"mode"`} {
		if !containsStr(got, want) {
			t.Fatalf("schema JSON missing %s:\n%s", want, got)
		}
	}
}

func mustJSONFieldNames(t *testing.T, s ResourceSchema) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
