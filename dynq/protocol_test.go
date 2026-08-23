package dynq

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---- 分页双模式归一化(§6.5 模式判定表) ----

func ip(n int) *int       { return &n }
func sp(s string) *string { return &s }

func TestNormalizePagination(t *testing.T) {
	meta := testMeta() // default 20 / max 200

	// 缺省 → offset 首页,默认 page_size
	limit, offset, after, err := NormalizePagination(meta, nil)
	if err != nil || limit != 20 || offset != 0 || after != nil {
		t.Fatalf("default: %v %v %v %v", limit, offset, after, err)
	}

	// page=3, page_size=50 → offset 100
	_, offset, _, err = NormalizePagination(meta, &Pagination{Page: ip(3), PageSize: ip(50)})
	if err != nil || offset != 100 {
		t.Fatalf("offset: %v %v", offset, err)
	}

	// after 非空 → 游标模式,page 缺省或 1 均可,offset 归零
	limit, offset, after, err = NormalizePagination(meta, &Pagination{After: sp("cur")})
	if err != nil || limit != 20 || offset != 0 || after == nil {
		t.Fatalf("cursor mode: %v %v %v %v", limit, offset, after, err)
	}
	_, offset, _, err = NormalizePagination(meta, &Pagination{Page: ip(1), After: sp("cur")})
	if err != nil || offset != 0 {
		t.Fatalf("cursor page=1: %v %v", offset, err)
	}

	// after 空串 → 400
	if _, _, _, err := NormalizePagination(meta, &Pagination{After: sp("")}); err == nil {
		t.Fatal("empty after rejected")
	}
	// after 与 page>1 混用 → 400
	if _, _, _, err := NormalizePagination(meta, &Pagination{Page: ip(2), After: sp("cur")}); err == nil {
		t.Fatal("after+page>1 rejected")
	}
	// page ≤ 0
	if _, _, _, err := NormalizePagination(meta, &Pagination{Page: ip(0)}); err == nil {
		t.Fatal("page=0 rejected (explicit)")
	}
	if _, _, _, err := NormalizePagination(meta, &Pagination{Page: ip(-1)}); err == nil {
		t.Fatal("page<0 rejected")
	}
	// page_size 显式 ≤ 0
	if _, _, _, err := NormalizePagination(meta, &Pagination{PageSize: ip(0)}); err == nil {
		t.Fatal("explicit page_size=0 rejected")
	}
	if _, _, _, err := NormalizePagination(meta, &Pagination{PageSize: ip(-5)}); err == nil {
		t.Fatal("negative page_size rejected")
	}
	// page_size 超上限
	if _, _, _, err := NormalizePagination(meta, &Pagination{PageSize: ip(201)}); err == nil {
		t.Fatal("page_size over max rejected")
	}
	// offset 超上限((502-1) * 200 = 100200 > 100000)
	if _, _, _, err := NormalizePagination(meta, &Pagination{Page: ip(502), PageSize: ip(200)}); err == nil {
		t.Fatal("offset over limit rejected")
	}
	// 恰在上限((501-1) * 200 = 100000)
	if _, _, _, err := NormalizePagination(meta, &Pagination{Page: ip(501), PageSize: ip(200)}); err != nil {
		t.Fatalf("offset at limit must pass: %v", err)
	}
	// 游标模式不受 offset 上限约束(深翻页无累计限制)
	if _, _, _, err := NormalizePagination(meta, &Pagination{After: sp("cur")}); err != nil {
		t.Fatalf("cursor mode unaffected: %v", err)
	}
}

func TestCheckPaginationTypedGate(t *testing.T) {
	meta := testMeta()
	// Limit=0 → 表默认
	if l, err := CheckPagination(meta, 0, 0, nil); err != nil || l != 20 {
		t.Fatalf("limit default: %v %v", l, err)
	}
	// Limit 超上限
	if _, err := CheckPagination(meta, 201, 0, nil); err == nil {
		t.Fatal("limit over max rejected")
	}
	// After 与 Offset 并存
	after := sp("cur")
	if _, err := CheckPagination(meta, 20, 100, after); err == nil {
		t.Fatal("after+offset rejected")
	}
	if _, err := CheckPagination(meta, 20, 0, after); err != nil {
		t.Fatalf("after with zero offset ok: %v", err)
	}
	// After 空串
	if _, err := CheckPagination(meta, 20, 0, sp("")); err == nil {
		t.Fatal("empty after rejected")
	}
	// offset 超上限
	if _, err := CheckPagination(meta, 20, MaxOffset+1, nil); err == nil {
		t.Fatal("offset over limit rejected")
	}
}

// ---- fields 解析与投影并集 ----

func TestParseSelectFields(t *testing.T) {
	meta := testMeta()
	cfg, _ := NewQueryConfig(meta)

	// 缺省 nil → 默认全量投影
	if f, err := ParseSelectFields(nil, cfg, meta); err != nil || f != nil {
		t.Fatalf("nil fields: %v %v", f, err)
	}
	// 显式空数组拒绝
	if _, err := ParseSelectFields([]string{}, cfg, meta); err == nil {
		t.Fatal("empty fields rejected")
	}
	// 未知 / hidden / 禁用
	if _, err := ParseSelectFields([]string{"ghost"}, cfg, meta); err == nil {
		t.Fatal("unknown field rejected")
	}
	if _, err := ParseSelectFields([]string{"password"}, cfg, meta); err == nil {
		t.Fatal("hidden field rejected")
	} else if e := AsError(err); !strings.Contains(e.Reason, "hidden") {
		t.Fatalf("reason: %s", e.Reason)
	}
	disabled, _ := NewQueryConfig(meta, DisableSelect("name"))
	if _, err := ParseSelectFields([]string{"name"}, disabled, meta); err == nil {
		t.Fatal("disabled field rejected")
	}
	// 去重保序
	f, err := ParseSelectFields([]string{"name", "id", "name"}, cfg, meta)
	if err != nil || len(f) != 2 || f[0] != "name" || f[1] != "id" {
		t.Fatalf("dedupe: %v %v", f, err)
	}
}

func TestSelectColumnsProjectionUnion(t *testing.T) {
	meta := testMeta()
	cfg, _ := NewQueryConfig(meta)
	// 窄投影 name:实际投影 = {name} ∪ 有效排序(created_at DESC + id)
	seq, _ := ResolveSort(meta, cfg, nil)
	cols := SelectColumns(meta, []FieldName{"name"}, seq)
	want := []string{"name", "created_at", "id"}
	if len(cols) != len(want) {
		t.Fatalf("cols: %v", cols)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Fatalf("cols: %v want %v", cols, want)
		}
	}
	// nil → 全部非 hidden(不含 password)∪ 排序去重
	cols = SelectColumns(meta, nil, seq)
	if len(cols) != len(meta.Fields)-1 { // 全部 7 列 - hidden 1
		t.Fatalf("all cols: %v", cols)
	}
	for _, c := range cols {
		if c == "password" {
			t.Fatal("hidden column must never be projected")
		}
	}
	// 排序列已在 fields 中 → 不重复
	seqName, _ := ResolveSort(meta, cfg, []OrderBy{{Field: "name"}})
	cols = SelectColumns(meta, []FieldName{"name", "id"}, seqName)
	if len(cols) != 2 || cols[0] != "name" || cols[1] != "id" {
		t.Fatalf("no dup: %v", cols)
	}
}

func TestValidateTypedSelectFields(t *testing.T) {
	meta := testMeta()
	cfg, _ := NewQueryConfig(meta)
	if err := ValidateTypedSelectFields([]FieldName{"name"}, cfg, meta); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTypedSelectFields([]FieldName{"password"}, cfg, meta); err == nil {
		t.Fatal("hidden rejected")
	}
	if err := ValidateTypedSelectFields([]FieldName{"ghost"}, cfg, meta); err == nil {
		t.Fatal("unknown rejected")
	}
}

// ---- 严格解码 ----

func TestDecodeQuery(t *testing.T) {
	q, err := DecodeQuery(strings.NewReader(`{
		"fields": ["id","name"],
		"where": [{"field":"name","op":"contains","value":"al"}],
		"sort": [{"field":"createdAt","order":"desc"}],
		"pagination": {"page":2,"pageSize":50,"includeTotal":false}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Fields) != 2 || len(q.Where) != 1 || len(q.Sort) != 1 {
		t.Fatalf("q: %+v", q)
	}
	if IncludeTotalOf(q.Pagination) {
		t.Fatal("include_total should decode to false")
	}
	if !IncludeTotalOf(nil) {
		t.Fatal("nil pagination include_total defaults true")
	}

	// 未知键拒绝
	if _, err := DecodeQuery(strings.NewReader(`{"fields":["id"],"extra":1}`)); err == nil {
		t.Fatal("unknown key rejected")
	}
	// 尾随数据
	if _, err := DecodeQuery(strings.NewReader(`{}`)); err != nil {
		t.Fatalf("empty envelope ok: %v", err)
	}
	if _, err := DecodeQuery(strings.NewReader(`{} {}`)); err == nil {
		t.Fatal("trailing data rejected")
	}
	// 请求体超 1MB
	big := `{"where":[{"field":"name","op":"eq","value":"` + strings.Repeat("a", MaxBodyBytes) + `"}]}`
	if _, err := DecodeQuery(strings.NewReader(big)); err == nil {
		t.Fatal("oversized body rejected")
	}
}

// ---- schema 投影与幂等 ----

func TestBuildSchemaProjection(t *testing.T) {
	meta := testMeta()
	full := BuildSchema(meta, nil)
	if len(full.Fields) != len(meta.Fields)-1 { // hidden 不出现
		t.Fatalf("fields: %d", len(full.Fields))
	}
	for _, f := range full.Fields {
		if f.Field == "password" {
			t.Fatal("hidden leaked to schema")
		}
	}
	if full.Pagination.DefaultPageSize != 20 || full.Pagination.MaxPageSize != 200 ||
		len(full.Pagination.Modes) != 2 || !full.Pagination.IncludeTotal {
		t.Fatalf("pagination: %+v", full.Pagination)
	}
	if len(full.DefaultSort) != 1 || full.DefaultSort[0].Field != "createdAt" {
		t.Fatalf("default sort: %+v", full.DefaultSort)
	}

	// 完全禁用 → 字段从 schema 消失
	cfg, err := NewQueryConfig(meta, DisableWhere("name"), DisableSort("name"), DisableSelect("name"))
	if err != nil {
		t.Fatal(err)
	}
	narrowed := BuildSchema(meta, cfg)
	for _, f := range narrowed.Fields {
		if f.Field == "name" {
			t.Fatal("fully disabled field must disappear")
		}
	}
	// 部分禁用:where 禁用 → ops 清空但 sortable 仍在
	cfg2, _ := NewQueryConfig(meta, DisableWhere("name"))
	partial := BuildSchema(meta, cfg2)
	for _, f := range partial.Fields {
		if f.Field == "name" {
			if len(f.Ops) != 0 {
				t.Fatalf("ops cleared: %v", f.Ops)
			}
			if !f.Sortable {
				t.Fatal("sortable preserved")
			}
		}
	}

	// 幂等:同 opts 产出字节级相同 schema(Hash 作 ETag)
	if BuildSchema(meta, cfg).Hash() != BuildSchema(meta, cfg).Hash() {
		t.Fatal("schema hash deterministic")
	}
	if BuildSchema(meta, cfg).Hash() == BuildSchema(meta, nil).Hash() {
		t.Fatal("different disable views must differ in hash")
	}
}

// TestSchemaJSONFieldNames 锁定前端契约的 JSON 键名拼写:这些键是前端消费面,
// 重命名即破坏契约,必须显式改动此测试。
func TestSchemaJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(BuildSchema(testMeta(), nil))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	fields, ok := m["fields"].([]any)
	if !ok || len(fields) == 0 {
		t.Fatalf("fields: %v", m["fields"])
	}
	first := fields[0].(map[string]any)
	if _, ok := first["field"]; !ok {
		t.Fatalf("fields[].field missing, got keys: %v", jsonKeys(first))
	}
	for _, legacy := range []string{"key", "ui"} {
		if _, bad := first[legacy]; bad {
			t.Fatalf("legacy fields[].%s must not exist", legacy)
		}
	}
	pag, ok := m["pagination"].(map[string]any)
	if !ok {
		t.Fatalf("pagination: %v", m["pagination"])
	}
	for _, want := range []string{"defaultPageSize", "maxPageSize", "modes", "includeTotal"} {
		if _, ok := pag[want]; !ok {
			t.Fatalf("pagination.%s missing, got keys: %v", want, jsonKeys(pag))
		}
	}
	for _, legacy := range []string{"default_page_size", "max_page_size", "include_total", "default", "max"} {
		if _, bad := pag[legacy]; bad {
			t.Fatalf("legacy pagination.%s must not exist", legacy)
		}
	}
	if _, ok := first["dataType"]; !ok {
		t.Fatalf("fields[].dataType missing, got keys: %v", jsonKeys(first))
	}
	if _, bad := first["data_type"]; bad {
		t.Fatal("legacy fields[].data_type must not exist")
	}
}

func jsonKeys(m map[string]any) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
