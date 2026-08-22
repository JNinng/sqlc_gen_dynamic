package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// golden file 测试:夹具 request.json 是真实 sqlc v1.31.1 产出的
// GenerateRequest dump(见 bin/request_dump.json 生成方式),保证与宿主
// 真实形态一致(dump-first 原则)。

const goldenOptions = `{
  "package": "gen",
  "runtime_import": "sqlc_gen_dynamic/internal/dynq",
  "schema": "public",
  "default_ops": "none",
  "page_default": 20,
  "page_max": 200
}`

func loadGoldenRequest(t *testing.T) *pb.GenerateRequest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "request.json"))
	if err != nil {
		t.Fatalf("read request fixture: %v", err)
	}
	var req pb.GenerateRequest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b, &req); err != nil {
		t.Fatalf("unmarshal request fixture: %v", err)
	}
	req.PluginOptions = []byte(goldenOptions)
	return &req
}

// writeRawOnFail 把生成失败的原始源码落盘,便于排错。
func writeRawOnFail(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := err.Error()
	if idx := strings.Index(msg, "--- source ---"); idx >= 0 {
		raw := msg[idx+len("--- source ---"):]
		path := filepath.Join("testdata", "debug_raw.go")
		_ = os.WriteFile(path, []byte(raw), 0o644)
		t.Logf("raw source written to %s", path)
	}
}

func TestGenerateGolden(t *testing.T) {
	req := loadGoldenRequest(t)
	resp, err := Generate(context.Background(), req)
	writeRawOnFail(t, err)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden")
	update := os.Getenv("GOLDEN_UPDATE") != ""
	for _, f := range resp.Files {
		path := filepath.Join(goldenDir, f.Name)
		if update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, f.Contents, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("golden file missing (%s): %v (run with GOLDEN_UPDATE=1)", f.Name, rerr)
		}
		if string(want) != string(f.Contents) {
			t.Errorf("%s: generated output differs from golden (run GOLDEN_UPDATE=1 after review)", f.Name)
		}
	}
	if update {
		t.Log("golden updated")
	}
}

func TestGenerateTablesFilter(t *testing.T) {
	req := loadGoldenRequest(t)
	req.PluginOptions = []byte(`{
		"package": "gen",
		"runtime_import": "sqlc_gen_dynamic/internal/dynq",
		"tables": ["orders"]
	}`)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(resp.Files) != 1 || resp.Files[0].Name != "orders_dynamic_gen.go" {
		t.Fatalf("want only orders_dynamic_gen.go, got %v", fileNames(resp))
	}
}

// TestGenerateDSLFailures 覆盖全部生成期 fail 路径(错误快照)。
func TestGenerateDSLFailures(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*pb.GenerateRequest)
		wantErr string
	}{
		{
			name:    "hidden_combined_with_dynamic",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@dynamic: exact @hidden]") },
			wantErr: "@hidden cannot be combined",
		},
		{
			name:    "hidden_combined_with_sortable",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortable @hidden]") },
			wantErr: "@hidden cannot be combined",
		},
		{
			name:    "unknown_tag",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortble]") },
			wantErr: "unknown directive @sortble",
		},
		{
			name:    "unknown_column_scope_tag",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@page: 1/10]") },
			wantErr: "unknown directive @page",
		},
		{
			name:    "unclosed_bracket",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortable") },
			wantErr: "unclosed '['",
		},
		{
			name:    "op_not_supported_by_type",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "status", "状态 [@dynamic: gt]") },
			wantErr: "not supported by dataType enum",
		},
		{
			name:    "preset_intersect_to_empty",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "status", "状态 [@dynamic: range]") },
			wantErr: "declares no supported ops",
		},
		{
			name:    "null_op_on_not_null_column",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "created_at", "创建时间 [@dynamic: is_null]") },
			wantErr: "declared on NOT NULL column",
		},
		{
			name: "default_sort_target_not_sortable",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: email DESC @pk: id]")
			},
			wantErr: "must declare @sortable",
		},
		{
			name: "default_sort_target_hidden",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: id DESC @pk: id]")
				setComment(r, "users", "id", "ID [@hidden]")
			},
			wantErr: "must not be @hidden",
		},
		{
			name: "default_sort_direction_missing",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: created_at @pk: id]")
			},
			wantErr: "@default_sort wants",
		},
		{
			name: "page_default_exceeds_max",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: created_at DESC @pk: id @page: 300/200]")
			},
			wantErr: "exceeds max",
		},
		{
			name:    "missing_pk",
			mutate:  func(r *pb.GenerateRequest) { setTableComment(r, "users", "用户表 [@default_sort: created_at DESC]") },
			wantErr: "missing @pk",
		},
		{
			name: "pk_column_nullable",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@pk: age]")
			},
			wantErr: "must be NOT NULL",
		},
		{
			name: "camel_conflict",
			mutate: func(r *pb.GenerateRequest) {
				addColumn(r, "users", "created__at", "TIMESTAMPTZ", true)
			},
			wantErr: "fold to the same camelCase key",
		},
		{
			name:    "sortable_numeric",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "balance", "余额 [@sortable]") },
			wantErr: "@sortable not supported for numeric",
		},
		{
			name:    "tag_with_payload",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortable: yes]") },
			wantErr: "takes no payload",
		},
		{
			name: "unknown_table_in_options",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen","runtime_import":"x","tables":["ghosts"]}`)
			},
			wantErr: "not found in schema",
		},
		// ---- @api ----
		{
			name: "api_missing_endpoint",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: value_key=id label_key=name]")
			},
			wantErr: "@api wants",
		},
		{
			name: "api_missing_value_key",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: /users/options label_key=name]")
			},
			wantErr: "value_key and label_key are required",
		},
		{
			name: "api_missing_label_key",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: /users/options value_key=id]")
			},
			wantErr: "value_key and label_key are required",
		},
		{
			name: "api_bad_datatype",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "created_at", "创建时间 [@api: /x value_key=v label_key=l]")
			},
			wantErr: "@api not supported for dataType datetime",
		},
		{
			name: "api_hidden_combined",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: /users/options value_key=id label_key=name @hidden]")
			},
			wantErr: "@hidden cannot be combined",
		},
		{
			name: "api_unknown_key",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: /users/options valuekey=id label_key=name]")
			},
			wantErr: "unknown @api parameter",
		},
		{
			name: "api_bad_mode",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: /users/options value_key=id label_key=name mode=select]")
			},
			wantErr: "mode must be single|multi|search",
		},
		{
			name: "api_endpoint_with_query",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: /users/options?active=1 value_key=id label_key=name]")
			},
			wantErr: "must not contain a query string",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := loadGoldenRequest(t)
			tc.mutate(req)
			_, err := Generate(context.Background(), req)
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func fileNames(resp *pb.GenerateResponse) []string {
	var out []string
	for _, f := range resp.Files {
		out = append(out, f.Name)
	}
	return out
}

func findTable(req *pb.GenerateRequest, table string) *pb.Table {
	for _, s := range req.Catalog.Schemas {
		if s.Name != "public" {
			continue
		}
		for i, t := range s.Tables {
			if t.Rel.Name == table {
				return s.Tables[i]
			}
		}
	}
	return nil
}

func setComment(req *pb.GenerateRequest, table, column, comment string) {
	t := findTable(req, table)
	if t == nil {
		panic("table not found: " + table)
	}
	for _, c := range t.Columns {
		if c.Name == column {
			c.Comment = comment
			return
		}
	}
	panic("column not found: " + table + "." + column)
}

func setTableComment(req *pb.GenerateRequest, table, comment string) {
	t := findTable(req, table)
	if t == nil {
		panic("table not found: " + table)
	}
	t.Comment = comment
}

func addColumn(req *pb.GenerateRequest, table, column, typeName string, notNull bool) {
	t := findTable(req, table)
	if t == nil {
		panic("table not found: " + table)
	}
	t.Columns = append(t.Columns, &pb.Column{
		Name:    column,
		NotNull: notNull,
		Type:    &pb.Identifier{Name: typeName},
	})
}
