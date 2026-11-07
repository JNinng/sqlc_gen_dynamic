package dynq

import (
	"encoding/json"
	"testing"
)

// ---- options.ui api:mode 推导 ----

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

// ---- options.ui api:schema 输出 ----

func TestBuildSchemaAPI(t *testing.T) {
	meta := TableMeta{
		Resource: "orders",
		Fields: []FieldMeta{
			{Key: "id", Column: "id", DataType: DataTypeInteger, Sortable: true},
			{Key: "userId", Column: "user_id", DataType: DataTypeInteger,
				Ops: []Op{OpEq, OpIn},
				API: &FieldAPI{Endpoint: "/api/v1/users/options", ValueKey: "id", LabelKey: "name"}},
			// 显式 mode 声明。
			{Key: "ownerId", Column: "owner_id", DataType: DataTypeUUID,
				Ops: []Op{OpEq},
				API: &FieldAPI{Endpoint: "/api/v1/owners", ValueKey: "id", LabelKey: "name", Mode: "search"}},
			// UI 元数据不允许落在 @internal 列(生成期 fail),此处仅验证 internal 列不输出。
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
				t.Fatal("field without options.ui api must have nil api")
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

// ---- visibility:三态与缺省 optional,协议校验无感 ----

func TestBuildSchemaVisibility(t *testing.T) {
	meta := TableMeta{
		Resource: "users",
		Fields: []FieldMeta{
			{Key: "id", Column: "id", DataType: DataTypeInteger, Visibility: VisibilityAlways},
			{Key: "name", Column: "name", DataType: DataTypeString}, // 零值 → optional
			{Key: "secret", Column: "secret", DataType: DataTypeString, Visibility: VisibilityHidden},
			// 纯投影列(op 集为空、不可排序、非 internal)仍进入 fields。
			{Key: "note", Column: "note", DataType: DataTypeString},
			{Key: "password", Column: "password", DataType: DataTypeString, Hidden: true},
		},
		Tiebreaker:  "id",
		PageDefault: 20,
		PageMax:     100,
	}
	s := BuildSchema(meta, nil)
	got := map[string]string{}
	for _, f := range s.Fields {
		got[f.Field] = f.Visibility
	}
	if got["password"] != "" {
		t.Fatal("hidden field must not appear in schema")
	}
	for field, want := range map[string]string{
		"id":     "always",
		"name":   "optional",
		"secret": "hidden",
		"note":   "optional",
	} {
		if got[field] != want {
			t.Errorf("%s visibility: got %q want %q", field, got[field], want)
		}
	}
	// 契约键:visibility 存在,hideable/selectable 已移除。
	js := mustJSONFieldNames(t, s)
	if !containsStr(js, `"visibility":"hidden"`) {
		t.Fatalf("schema JSON must carry visibility:\n%s", js)
	}
	for _, gone := range []string{`"hideable"`, `"selectable"`} {
		if containsStr(js, gone) {
			t.Fatalf("retired schema key %s must not be emitted:\n%s", gone, js)
		}
	}
}

// TestBuildSchemaSelectDisabled 验证 SelectDisabled 从 fields 移除(优先于
// 可见性);where/sort 禁用仍是减法投影,不改变字段集合。
func TestBuildSchemaSelectDisabled(t *testing.T) {
	meta := TableMeta{
		Resource: "users",
		Fields: []FieldMeta{
			{Key: "id", Column: "id", DataType: DataTypeInteger},
			{Key: "name", Column: "name", DataType: DataTypeString, Ops: []Op{OpEq}, Sortable: true},
		},
		Tiebreaker:  "id",
		PageDefault: 20,
		PageMax:     100,
	}
	cfg, err := NewQueryConfig(meta, DisableSelect("name"), DisableWhere("name"), DisableSort("name"))
	if err != nil {
		t.Fatal(err)
	}
	s := BuildSchema(meta, cfg)
	if len(s.Fields) != 1 || s.Fields[0].Field != "id" {
		t.Fatalf("SelectDisabled field must be removed from schema: %v", s.Fields)
	}
	// where/sort 禁用只清投影,不移除字段。
	cfg2, err := NewQueryConfig(meta, DisableWhere("name"), DisableSort("name"))
	if err != nil {
		t.Fatal(err)
	}
	s2 := BuildSchema(meta, cfg2)
	for _, f := range s2.Fields {
		if f.Field == "name" {
			if f.Sortable || len(f.Ops) != 0 {
				t.Fatalf("where/sort disabled field must keep field but lose capabilities: %+v", f)
			}
			return
		}
	}
	t.Fatal("name must remain in schema when only where/sort are disabled")
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

// ---- UI 元数据(desc / options / order):schema 透传与排序 ----

// TestBuildSchemaAnnotationPassthrough 验证 options.ui 的 desc / options 透传与 omitempty:
// 已声明字段输出 desc/options 键;未声明字段的 JSON 不含这两个键
// (存量资源 schema 字节级不变,ETag hash 不漂移)。
func TestBuildSchemaAnnotationPassthrough(t *testing.T) {
	meta := TableMeta{
		Resource: "users",
		Fields: []FieldMeta{
			{Key: "status", Column: "status", DataType: DataTypeInteger, Sortable: true,
				Desc:    "0：禁用、1：启用",
				Options: []FieldOption{{Value: int64(0), Label: "禁用"}, {Value: int64(1), Label: "启用"}}},
			{Key: "name", Column: "name", DataType: DataTypeString, Sortable: true},
		},
		Tiebreaker:  "id",
		PageDefault: 20,
		PageMax:     100,
	}
	s := BuildSchema(meta, nil)
	var annotated, plain FieldSchema
	for _, f := range s.Fields {
		switch f.Field {
		case "status":
			annotated = f
		case "name":
			plain = f
		}
	}
	if annotated.Desc != "0：禁用、1：启用" {
		t.Fatalf("desc passthrough: %q", annotated.Desc)
	}
	if len(annotated.Options) != 2 || annotated.Options[0].Value != int64(0) || annotated.Options[0].Label != "禁用" ||
		annotated.Options[1].Value != int64(1) || annotated.Options[1].Label != "启用" {
		t.Fatalf("options passthrough: %+v", annotated.Options)
	}
	if plain.Desc != "" || plain.Options != nil {
		t.Fatalf("undeclared field must not carry annotations: %+v", plain)
	}
	// omitempty:未声明字段不得输出 desc/options 键(存量资源 schema 字节级
	// 不变,ETag hash 不漂移);单独 marshal 避免 defaultSort 的 "desc"(bool)
	// 干扰全 schema 文本匹配。
	pb, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(string(pb), `"desc"`) || containsStr(string(pb), `"options"`) {
		t.Fatalf("unset desc/options must be omitted: %s", pb)
	}
	// 已声明字段输出两键。
	ab, err := json.Marshal(annotated)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(string(ab), `"desc":"0：禁用、1：启用"`) {
		t.Fatalf("desc must be emitted: %s", ab)
	}
	// FieldOption 的 JSON 形态锁定(前端契约)。
	b, err := json.Marshal(annotated.Options)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `[{"value":0,"label":"禁用"},{"value":1,"label":"启用"}]` {
		t.Fatalf("field option json form: %s", b)
	}
}

// TestBuildSchemaOrderSorting 验证 options.ui order 排序语义:已标注升序在前(order: 0
// 属已标注,与 nil 指针可区分)、同号按声明序稳定、未标注保持声明序殿后、
// 负数合法、Hidden 字段不出现、hash 确定。
func TestBuildSchemaOrderSorting(t *testing.T) {
	n := func(v int) *int { return &v }
	meta := TableMeta{
		Resource: "users",
		Fields: []FieldMeta{
			{Key: "id", Column: "id", DataType: DataTypeInteger, Sortable: true},                    // 未标注,声明序殿后
			{Key: "email", Column: "email", DataType: DataTypeString, Sortable: true, Order: n(10)}, // 与 status 同号,声明靠后
			{Key: "status", Column: "status", DataType: DataTypeInteger, Sortable: true, Order: n(10)},
			{Key: "age", Column: "age", DataType: DataTypeInteger, Sortable: true, Order: n(-1)},                       // 负数,排最前
			{Key: "nickname", Column: "nickname", DataType: DataTypeString, Sortable: true, Order: n(0)},               // 0 属已标注,非 nil
			{Key: "name", Column: "name", DataType: DataTypeString, Sortable: true},                                    // 未标注
			{Key: "password", Column: "password", DataType: DataTypeString, Sortable: true, Order: n(5), Hidden: true}, // Hidden,不出现
		},
		Tiebreaker:  "id",
		PageDefault: 20,
		PageMax:     100,
	}
	s := BuildSchema(meta, nil)
	var got []string
	for _, f := range s.Fields {
		if f.Field == "password" {
			t.Fatal("hidden annotated field must not appear in schema")
		}
		got = append(got, f.Field)
	}
	want := []string{"age", "nickname", "email", "status", "id", "name"} // 同号 10:email 声明序在前
	if len(got) != len(want) {
		t.Fatalf("fields: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order: got %v want %v", got, want)
		}
	}
	// 排序不破坏确定性纯函数:同输入同 hash。
	if s.Hash() != BuildSchema(meta, nil).Hash() {
		t.Fatal("schema hash deterministic under order sorting")
	}
	// 不同 order 视图 hash 可区分。
	unordered := meta
	for i := range unordered.Fields {
		unordered.Fields[i].Order = nil
	}
	if s.Hash() == BuildSchema(unordered, nil).Hash() {
		t.Fatal("reordered schema must differ in hash")
	}
}
