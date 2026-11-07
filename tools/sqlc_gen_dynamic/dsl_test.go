package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// dsl_test.go:注释 DSL 的生成期 fail 路径、括号配平、options.ui / options.page
// manifest 与封闭集合锁定。
// golden 比对见 golden_test.go,更新面产物见 gen_update_test.go。

// TestDirectiveOpSetsFrozen 锁定 @scope / @filter 的封闭 op 集合:防止
// 白名单被无意扩张(neq / not_in / in 等权限事故配方不得进入)。
func TestDirectiveOpSetsFrozen(t *testing.T) {
	assertSet := func(name string, got, want map[string]bool) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: size %d, want %d (%v)", name, len(got), len(want), got)
		}
		for op := range want {
			if !got[op] {
				t.Errorf("%s: missing %q", name, op)
			}
		}
	}
	assertSet("scopeOps", scopeOps, map[string]bool{
		"in": true, "in_any": true, "eq": true, "gt": true, "gte": true, "lt": true, "lte": true,
	})
	assertSet("filterOps", filterOps, map[string]bool{
		"eq": true, "gt": true, "gte": true, "lt": true, "lte": true, "is_null": true, "not_null": true,
	})
}

// TestGenerateDSLFailures 覆盖全部生成期 fail 路径(错误快照)。
// wantSuccess=true 的 case 是放行分支的反向锚点(同构造下必须生成成功)。
func TestGenerateDSLFailures(t *testing.T) {
	const cols = `"runtime_import":"github.com/jninng/sqlc_gen_dynamic/dynq"`
	cases := []struct {
		name    string
		mutate  func(*pb.GenerateRequest)
		wantErr string
		// wantSuccess 标记放行分支:必须生成成功且满足断言,与 fail 分支互斥。
		wantSuccess bool
	}{
		{
			name:    "internal_combined_with_dynamic",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@dynamic: exact @internal]") },
			wantErr: "@internal cannot be combined with @dynamic",
		},
		{
			name:    "hidden_old_name_unknown",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@dynamic: exact @hidden]") },
			wantErr: "unknown directive @hidden",
		},
		{
			name:    "selectable_old_name_unknown",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@selectable]") },
			wantErr: "unknown directive @selectable",
		},
		{
			name:    "sortable_removed",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortable]") },
			wantErr: "unknown directive @sortable",
		},
		{
			name:    "unknown_tag",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortble]") },
			wantErr: "unknown directive @sortble",
		},
		{
			name:    "unknown_tag_lists_new_names_only",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@selectablee]") },
			wantErr: "allowed for column: @dynamic, @internal, @updatable",
		},
		{
			name:    "page_on_column_unknown",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@page: 1/10]") },
			wantErr: "unknown directive @page",
		},
		{
			name:    "unclosed_bracket",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@dynamic: exact") },
			wantErr: "unclosed '['",
		},
		{
			name:    "bare_close_bracket",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 ] [@dynamic: exact]") },
			wantErr: "unmatched ']'",
		},
		{
			name:    "internal_with_payload",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@internal: yes]") },
			wantErr: "@internal takes no payload",
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
		// ---- 枚举数组:读能力面生成期拒绝,非法 SQL 不再流到运行期 ----
		{
			name: "enum_array_dynamic_rejected",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					Comment: "状态组 [@dynamic: membership]",
				})
			},
			wantErr: `array type "user_status" is not supported for @dynamic (enum arrays are projection-only)`,
		},
		{
			name: "enum_array_sort_rejected",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					Comment: "状态组 [@dynamic: sort]",
				})
			},
			wantErr: "not supported for sorting",
		},
		{
			name: "enum_array_projection_only_allowed",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					Comment: "状态组",
				})
			},
			wantSuccess: true,
		},
		{
			// 策略驱动的拒绝分支——文案点名 default_ops 触发器并给出出路。
			name: "enum_array_default_ops_rejected",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					Comment: "状态组",
				})
				r.PluginOptions = []byte(`{"package":"gen","runtime_import":"github.com/jninng/sqlc_gen_dynamic/dynq","default_ops":"exact"}`)
			},
			wantErr: `array type "user_status" is not supported for dynamic filtering granted by default_ops; mark the column @internal, or set default_ops: none`,
		},
		{
			name: "default_sort_target_implicit_sortable",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: email DESC @pk: id]")
			},
			wantSuccess: true,
		},
		{
			name: "default_sort_target_internal",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: id DESC @pk: id]")
				setComment(r, "users", "id", "ID [@internal]")
			},
			wantErr: "must not be @internal",
		},
		{
			name: "default_sort_direction_missing",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: created_at @pk: id]")
			},
			wantErr: "@default_sort wants",
		},
		{
			name:    "missing_pk",
			mutate:  func(r *pb.GenerateRequest) { setTableComment(r, "users", "用户表 [@default_sort: created_at DESC]") },
			wantErr: "missing @pk",
		},
		{
			name: "pk_column_nullable",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@pk: birth_date]")
			},
			wantErr: "must be NOT NULL",
		},
		{
			name: "pk_jsonb_not_cursor_encodable",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					if c.Name == "meta" {
						c.NotNull = true
						c.Comment = ""
					}
				}
				users.Comment = "用户表 [@pk: meta]"
			},
			wantErr: `has unsupported type "jsonb" for cursor encoding`,
		},
		{
			// 枚举数组列的 DataType 同为 "enum",仅按 DataType 的白名单会
			// 穿透:数组列一律不可作 tiebreaker(游标取值按标量形态发射,
			// 对切片字段不可编译)。
			name: "pk_enum_array_rejected",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					NotNull: true,
					Comment: "状态组",
				})
				users.Comment = "用户表 [@pk: statuses]"
			},
			wantErr: "must not be an array",
		},
		{
			name: "camel_conflict",
			mutate: func(r *pb.GenerateRequest) {
				addColumn(r, "users", "created__at", "TIMESTAMPTZ", true)
			},
			wantErr: "fold to the same camelCase key",
		},
		{
			// sort 能力位复用 int_array 拒绝规则:数组 seek 谓词无法参数化。
			name:    "sort_int_array",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "tags", "标签 [@dynamic: sort]") },
			wantErr: "int_array columns cannot be sorted",
		},
		{
			// sort 能力位复用 jsonb 拒绝规则:结构化比较对前端无可解释语义。
			name:    "sort_jsonb",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "meta", "元数据 [@dynamic: sort]") },
			wantErr: "jsonb columns cannot be sorted",
		},
		{
			name: "sort_unmapped_type",
			mutate: func(r *pb.GenerateRequest) {
				addColumn(r, "users", "extras", "inet", false)
				setComment(r, "users", "extras", "扩展 [@dynamic: sort]")
			},
			wantErr: "sorting requires a mappable type",
		},
		{
			name:    "dynamic_sort_with_payload",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@dynamic: sort: yes]") },
			wantErr: "unknown @dynamic item",
		},
		{
			name: "unknown_table_in_options",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen","runtime_import":"x","tables":["ghosts"]}`)
			},
			wantErr: "not found in schema",
		},
		// ---- default_ops 死配置:无 @pk 入选表不再被静默忽略 ----
		{
			name: "default_ops_without_pk_rejected",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen","runtime_import":"github.com/jninng/sqlc_gen_dynamic/dynq","default_ops":"exact"}`)
			},
			wantErr: `default_ops "exact" would grant read-side ops but the table has no @pk`,
		},
		// ---- @scope ----
		{
			name: "scope_missing_guards",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: @pk: id]")
			},
			wantErr: "@scope wants",
		},
		{
			name: "scope_unknown_op",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name like fn_x(user_id:varchar) @pk: id]")
			},
			wantErr: `@scope op "like" not allowed`,
		},
		{
			name: "scope_neq_not_allowed",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name neq fn_x(user_id:varchar) @pk: id]")
			},
			wantErr: `@scope op "neq" not allowed`,
		},
		{
			name: "scope_op_not_in_allowlist",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name not_in fn_x(user_id:varchar) @pk: id]")
			},
			wantErr: "want one of: eq, gt, gte, in, in_any, lt, lte",
		},
		{
			name: "scope_missing_parens",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name eq fn_x @pk: id]")
			},
			wantErr: "@scope wants '('",
		},
		{
			name: "scope_unclosed_paren",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in fn_x(user_id:varchar @pk: id]")
			},
			wantErr: "missing ')'",
		},
		{
			name: "scope_unknown_column",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: ghost in fn_x(user_id:varchar) @pk: id]")
			},
			wantErr: `@scope column "ghost" not found`,
		},
		{
			name: "scope_column_guarded_twice",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in fn_a(user_id:varchar) name eq fn_b(user_id:varchar) @pk: id]")
			},
			wantErr: "guarded twice",
		},
		{
			name: "scope_bad_fn_name",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in Fn-A(user_id:varchar) @pk: id]")
			},
			wantErr: "not a plain (optionally schema-qualified) lowercase identifier",
		},
		{
			name: "scope_unknown_param_type",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in fn_x(user_id:text) @pk: id]")
			},
			wantErr: `parameter type "text" not in vocabulary`,
		},
		{
			name: "scope_param_type_conflict",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in fn_a(uid:varchar) email in fn_b(uid:int) @pk: id]")
			},
			wantErr: "conflicting types",
		},
		{
			name: "scope_param_go_name_fold",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in fn_a(user_id:varchar) email in fn_b(user__id:int) @pk: id]")
			},
			wantErr: "fold to the same Go field",
		},
		{
			name: "scope_arg_missing_type",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in fn_x(user_id) @pk: id]")
			},
			wantErr: "':' after parameter name",
		},
		{
			name: "scope_string_literal_with_quote",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@scope: name in fn_x('a''b') @pk: id]")
			},
			wantErr: "wants ',' or ')'",
		},
		{
			name: "scope_not_allowed_on_column",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "users", "name", "姓名 [@scope: x eq fn_y()]")
			},
			wantErr: "unknown directive @scope",
		},
		// ---- @filter ----
		{
			name: "filter_unknown_op",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: deleted_at between 1 @pk: id]")
			},
			wantErr: `@filter op "between" not allowed`,
		},
		{
			name: "filter_neq_not_allowed",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: name neq 'x' @pk: id]")
			},
			wantErr: `@filter op "neq" not allowed`,
		},
		{
			name: "filter_in_not_allowed",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: name in 'x' @pk: id]")
			},
			wantErr: `@filter op "in" not allowed`,
		},
		{
			name: "filter_missing_literal",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "orders", "订单表 [@filter: amount_cents gte @pk: id]")
			},
			wantErr: "wants exactly one literal",
		},
		{
			name: "filter_literal_not_scalar",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: status eq paid @pk: id]")
			},
			wantErr: `got "paid"`,
		},
		{
			name: "filter_integer_out_of_range",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: age eq 99999999999999999999 @pk: id]")
			},
			wantErr: `integer literal "99999999999999999999" out of range`,
		},
		{
			name: "filter_trailing_column_without_op",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: deleted_at is_null age @pk: id]")
			},
			wantErr: `wants an operator after column "age"`,
		},
		{
			name: "filter_is_null_with_negative_int",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: deleted_at is_null -1 @pk: id]")
			},
			wantErr: "takes no value",
		},
		{
			name: "filter_bad_column",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: DeletedAt is_null @pk: id]")
			},
			wantErr: "not a snake_case identifier",
		},
		{
			name: "filter_duplicate_column",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: deleted_at is_null deleted_at not_null @pk: id]")
			},
			wantErr: "filtered twice",
		},
		{
			name: "filter_is_null_with_value",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: deleted_at is_null 'x' @pk: id]")
			},
			wantErr: "takes no value",
		},
		{
			name: "filter_unknown_column",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@filter: ghost is_null @pk: id]")
			},
			wantErr: `@filter column "ghost" not found`,
		},
		{
			name: "filter_not_allowed_on_column",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "users", "name", "姓名 [@filter: id eq 1]")
			},
			wantErr: "unknown directive @filter",
		},
		// ---- @updatable(四形态)----
		{
			name:    "updatable_unknown_item",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@updatable: bogus]") },
			wantErr: `unknown @updatable item "bogus"`,
		},
		{
			name:    "updatable_empty_item",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@updatable: eq, ,gt]") },
			wantErr: "empty @updatable item",
		},
		{
			name:    "updatable_reserved_mode_mixed",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@updatable: set, eq]") },
			wantErr: `reserved mode "set" must appear alone`,
		},
		{
			name:    "updatable_auto_keyword_removed",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@updatable: auto]") },
			wantErr: "@updatable: auto was removed",
		},
		{
			name:    "updatable_false_removed",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@updatable: false]") },
			wantErr: "@updatable: false was removed",
		},
		{
			name:    "updatable_where_renamed",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@updatable: where]") },
			wantErr: "@updatable: where was renamed to locate",
		},
		{
			name:    "updatable_op_not_supported_by_type",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "status", "状态 [@updatable: gt]") },
			wantErr: `@updatable op "gt" not supported by dataType enum`,
		},
		{
			name:    "updatable_null_op_on_not_null",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@updatable: is_null]") },
			wantErr: "declared on NOT NULL column",
		},
		{
			name:    "updatable_empty_expansion",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "status", "状态 [@updatable: range]") },
			wantErr: "@updatable declares no supported ops",
		},
		{
			name:    "updatable_set_on_pk_rejected",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "id", "ID [@updatable: set]") },
			wantErr: "is not allowed on the @pk column",
		},
		{
			name:    "updatable_locate_on_pk_rejected",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "id", "ID [@updatable: locate]") },
			wantErr: "is not allowed on the @pk column",
		},
		{
			name:    "updatable_bare_on_pk_rejected",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "id", "ID [@updatable]") },
			wantErr: "is not allowed on the @pk column",
		},
		{
			name: "updatable_unmapped_type",
			mutate: func(r *pb.GenerateRequest) {
				addColumn(r, "users", "payload", "bytea", true)
				setComment(r, "users", "payload", "载荷 [@updatable]")
			},
			wantErr: "not supported for dynamic columns",
		},
		{
			name: "updatable_enum_array",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					Comment: "状态组 [@updatable]",
				})
			},
			wantErr: `array type "user_status" is not supported for @updatable`,
		},
		{
			name: "set_capable_without_locate_no_pk",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					c.Comment = ""
				}
				setComment(r, "users", "name", "姓名 [@updatable: set]")
				users.Comment = "用户表"
			},
			wantErr: "requires at least one locate-capable @updatable",
		},
		{
			name: "locate_capable_without_set",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					c.Comment = ""
				}
				setComment(r, "users", "name", "姓名 [@updatable: locate]")
				users.Comment = "用户表 [@pk: id]"
			},
			wantErr: "requires at least one set-capable @updatable",
		},
		{
			name: "nopk_read_directive",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "sys_resource_grant", "resource", "资源 [@dynamic: eq @updatable: locate]")
			},
			// 完整上下文文案断言(missing @pk 系列保留一条全文):
			wantErr: "table sys_resource_grant: missing @pk directive in table comment (primary key is required for deterministic pagination)",
		},
		{
			name: "nopk_sort_directive",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "sys_resource_grant", "note", "备注 [@dynamic: sort @updatable: set]")
			},
			wantErr: "missing @pk",
		},
		{
			name: "nopk_filter_unknown_column",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "sys_resource_grant", "共享授权 [@filter: ghost is_null]")
			},
			wantErr: `@filter column "ghost" not found`,
		},
		{
			name: "nopk_scope_directive",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "sys_resource_grant", "共享授权 [@scope: note eq fn_x(user_id:varchar)]")
			},
			wantErr: "missing @pk",
		},
		{
			name: "nopk_default_sort_directive",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "sys_resource_grant", "共享授权 [@default_sort: note ASC]")
			},
			wantErr: "missing @pk",
		},
		{
			name: "page_table_directive_removed",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "sys_resource_grant", "共享授权 [@page: 10/20]")
			},
			wantErr: "unknown directive @page",
		},
		{
			name: "update_table_directive_removed",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@update @pk: id]")
			},
			wantErr: "unknown directive @update",
		},
		{
			name: "updatable_at_table_level",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@updatable @pk: id]")
			},
			wantErr: "allowed for table: @default_sort, @filter, @pk, @scope",
		},
		// ---- options.ui manifest fail-closed ----
		{
			name: "ui_unknown_table",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"ghosts":{"id":{"label":"x"}}}}`)
			},
			wantErr: "ui.ghosts: table not found",
		},
		{
			name: "ui_unknown_column",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"ghost":{"label":"x"}}}}`)
			},
			wantErr: "ui.users.ghost: column not found",
		},
		{
			name: "ui_internal_column",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"password":{"label":"x"}}}}`)
			},
			wantErr: "@internal column must not carry UI metadata",
		},
		{
			name: "ui_table_without_read_surface",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"sys_resource_grant":{"note":{"label":"x"}}}}`)
			},
			wantErr: "no read surface",
		},
		{
			name: "ui_table_not_selected",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"tables":["orders"],"ui":{"users":{"name":{"label":"x"}}}}`)
			},
			wantErr: "not selected for generation",
		},
		{
			name: "ui_bad_visibility",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"name":{"visibility":"sometimes"}}}}`)
			},
			wantErr: "visibility must be always|optional|hidden",
		},
		{
			name: "ui_unknown_key",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"name":{"lable":"x"}}}}`)
			},
			wantErr: `unknown field "lable"`,
		},
		{
			name: "ui_empty_column_entry",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"name":{}}}}`)
			},
			wantErr: "entry must not be empty",
		},
		{
			name: "ui_null_table_entry",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":null}}`)
			},
			wantErr: "entry must not be null",
		},
		{
			name: "ui_options_and_api_together",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"role":{"options":[{"value":"admin","label":"A"}],"api":{"endpoint":"/x","value_key":"id","label_key":"name"}}}}}`)
			},
			wantErr: "options cannot be combined with api",
		},
		{
			name: "ui_options_on_enum_column",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"status":{"options":[{"value":"active","label":"启用"}]}}}}`)
			},
			wantErr: "options not supported for dataType enum",
		},
		{
			name: "ui_options_value_not_integer",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"age":{"options":[{"value":"x","label":"A"}]}}}}`)
			},
			wantErr: "value must be an integer",
		},
		{
			name: "ui_api_bad_datatype",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"ui":{"users":{"created_at":{"api":{"endpoint":"/x","value_key":"id","label_key":"name"}}}}}`)
			},
			wantErr: "not supported for dataType datetime",
		},
		// ---- options.page ----
		{
			name: "page_global_default_exceeds_max",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"page":{"default":300,"max":200}}`)
			},
			wantErr: "page.default 300 exceeds page.max 200",
		},
		{
			name: "page_table_unknown",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"page":{"tables":{"ghosts":{"default":10}}}}`)
			},
			wantErr: "page.tables.ghosts: table not found",
		},
		{
			name: "page_table_not_selected",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"tables":["orders"],"page":{"tables":{"users":{"default":10}}}}`)
			},
			wantErr: "not selected for generation",
		},
		{
			name: "page_table_invalid_range",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"page":{"tables":{"users":{"default":500,"max":100}}}}`)
			},
			wantErr: "page.tables.users.default 500 exceeds page.tables.users.max 100",
		},
		{
			name: "page_global_default_zero",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"page":{"default":0}}`)
			},
			wantErr: "page.default must be a positive integer",
		},
		{
			name: "page_table_max_negative",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"page":{"tables":{"users":{"max":-1}}}}`)
			},
			wantErr: "page.tables.users.max must be a positive integer",
		},
		{
			name: "default_ops_has_preset_removed",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"default_ops":"has"}`)
			},
			wantErr: `invalid default_ops "has" (want none|exact|membership|range|fuzzy)`,
		},
		{
			name: "unknown_option_key",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen",` + cols + `,"page_default":20}`)
			},
			wantErr: `unknown field "page_default"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := loadGoldenRequest(t)
			tc.mutate(req)
			resp, err := Generate(context.Background(), req)
			if tc.wantSuccess {
				// 放行分支:必须生成成功。
				if err != nil {
					t.Fatalf("want success, got: %v", err)
				}
				src := mustFile(t, resp, "users_dynamic_gen.go")
				if strings.Contains(src, "WhereStatuses") || strings.Contains(src, "StatusesIn") {
					t.Error("enum array projection-only column must not generate filter surface")
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestGoTypeOverrideImportCollected 覆盖:带 import 的 go_type_overrides
// 必须把外部包收集进产物 import;且仅在生成面实际引用该列 GoType 时收集——
// 收集面过宽会让 import 悬空(未使用 import 产物不可编译)。
// 引用面取更新面 Set / Where 签名:读面值 op 与协议门收敛类型分叉会被
// 生成期拒绝(见 TestGoTypeOverrideDivergence),不可作为收集来源。
func TestGoTypeOverrideImportCollected(t *testing.T) {
	t.Run("referenced_collected", func(t *testing.T) {
		req := loadGoldenRequest(t)
		// 剥离读面 @dynamic,引用面由更新面签名承担:
		// org_id 显式清单(eq)→ Set + Where 签名引用 uuid.UUID;
		// balance set-only → Set 签名引用 decimal.Decimal。
		setComment(req, "users", "org_id", "[@updatable: eq]")
		setComment(req, "users", "balance", "[@updatable: set]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.org_id", "go_type": {"import": "github.com/google/uuid", "package": "uuid", "type_name": "UUID"}},
				{"column": "users.balance", "go_type": {"import": "github.com/shopspring/decimal", "type_name": "Decimal"}}
			]
		}`)
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		users := mustFile(t, resp, "users_dynamic_gen.go")
		for _, want := range []string{
			`uuid "github.com/google/uuid"`,
			`decimal "github.com/shopspring/decimal"`,
			"func (UserTable) SetOrgID(v *uuid.UUID) UserUpdateOption",
			"func (UserTable) WhereOrgIDEq(v uuid.UUID) UserUpdateOption",
			// Package 缺省取 import 路径末段(shopspring/decimal → decimal)。
			"func (UserTable) SetBalance(v *decimal.Decimal) UserUpdateOption",
		} {
			if !strings.Contains(users, want) {
				t.Errorf("override import surface missing %q", want)
			}
		}
	})

	t.Run("unreferenced_skipped", func(t *testing.T) {
		req := loadGoldenRequest(t)
		// meta 仅为 locate-capable(locate-only,矩阵仅 has):Where 方法签名硬编码
		// []byte,不引用 override 类型;birth_date 零能力引用。两者都不得收集。
		setComment(req, "users", "meta", "[@updatable: locate]")
		setComment(req, "users", "birth_date", "")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.meta", "go_type": {"import": "github.com/google/civil", "package": "civil", "type_name": "Date"}},
				{"column": "users.birth_date", "go_type": {"import": "example.com/x", "package": "x", "type_name": "T"}}
			]
		}`)
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		users := mustFile(t, resp, "users_dynamic_gen.go")
		if strings.Contains(users, "civil") || strings.Contains(users, "example.com/x") {
			t.Error("overrides on unreferenced columns must not be collected into imports")
		}
		// 收集面收窄不得误伤生成面:locate-only has 的定位方法照常生成。
		if !strings.Contains(users, "func (UserTable) WhereMetaHas(v []byte) UserUpdateOption") {
			t.Error("locate-only has column must keep its Where method")
		}
	})

	t.Run("read_side_collected", func(t *testing.T) {
		// 非分叉 override(枚举列 + models 限定同型,与默认映射一致)+ 读面值
		// op:锁定三项——table.column 精确限定不泄漏到 orders 的同名列 status、
		// models import 与跨包基础 import 去重恰一次、读面 Params 槽位呈
		// []models.UserStatus 形态。(该组合的 override 与 models 同包,由
		// 基础 import 承载,不经收集判定。)
		req := loadGoldenRequest(t)
		setComment(req, "users", "status", "状态 [@dynamic: exact, in]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"model_import": "example/db/models",
			"model_package": "models",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.status", "go_type": {"import": "example/db/models", "package": "models", "type_name": "UserStatus"}}
			]
		}`)
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		users := mustFile(t, resp, "users_dynamic_gen.go")
		if !strings.Contains(users, "[]models.UserStatus") {
			t.Error("read-side Params slot must reference the override type (collected via the read disjunct)")
		}
		if got := strings.Count(users, `models "example/db/models"`); got != 1 {
			t.Errorf("models import must appear exactly once, got %d", got)
		}
		// column 形态按 table.column 精确限定:不得泄漏到 orders 的同名列 status。
		orders := mustFile(t, resp, "orders_dynamic_gen.go")
		if !strings.Contains(orders, "models.OrderStatus") || strings.Contains(orders, "models.UserStatus") {
			t.Error("override scoped to users.status must not leak onto orders.status")
		}
	})
}

// TestEnumArrayRejectedScope:枚举数组的能力面拒绝只作用于入选表——
// 被 options.tables 排除的表,其列声明与 default_ops 策略本不生效,不因此 fail。
func TestEnumArrayRejectedScope(t *testing.T) {
	poison := func(req *pb.GenerateRequest) {
		users := findTable(req, "users")
		users.Columns = append(users.Columns, &pb.Column{
			Name:    "statuses",
			IsArray: true,
			Type:    &pb.Identifier{Name: "user_status"},
			Comment: "状态组",
		})
	}
	assert := func(t *testing.T, resp *pb.GenerateResponse, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("enum array in an excluded table must not fail generation: %v", err)
		}
		if len(resp.Files) != 1 || resp.Files[0].Name != "orders_dynamic_gen.go" {
			t.Fatalf("want only orders_dynamic_gen.go, got %v", fileNames(resp))
		}
	}

	t.Run("explicit_dynamic_excluded", func(t *testing.T) {
		req := loadGoldenRequest(t)
		poison(req)
		setComment(req, "users", "statuses", "状态组 [@dynamic: in]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"tables": ["orders"]
		}`)
		resp, err := Generate(context.Background(), req)
		assert(t, resp, err)
	})

	t.Run("default_ops_granted_excluded", func(t *testing.T) {
		// default_ops 授予型违规在被排除表上同样不生效。
		req := loadGoldenRequest(t)
		poison(req)
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"default_ops": "exact",
			"tables": ["orders"]
		}`)
		resp, err := Generate(context.Background(), req)
		assert(t, resp, err)
	})
}

// TestOverrideModelImportPackageConflict:override 与 model_import
// 同包但 import/package 不一致时,生成期前置拒绝(否则错误落在产物编译期)。
func TestOverrideModelImportPackageConflict(t *testing.T) {
	base := `{
		"package": "gen",
		"model_import": "example/db/models",
		"model_package": "models",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"go_type_overrides": [%s]
	}`
	// model_package 与 import 路径末段不一致:缺省 package 的派生值(末段
	// "models")同样触发冲突。
	derivedBase := `{
		"package": "gen",
		"model_import": "example/db/models",
		"model_package": "m",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"go_type_overrides": [
			{"column": "users.org_id", "go_type": {"import": "example/db/models", "type_name": "OrgRef"}}
		]
	}`
	cases := []struct {
		name    string
		options string
	}{
		{
			// import 指向 models 包而 package 别名不一致:类型前缀与 import 别名失配。
			name:    "import_matches_package_mismatch",
			options: fmt.Sprintf(base, `{"column": "users.org_id", "go_type": {"import": "example/db/models", "package": "other", "type_name": "OrgRef"}}`),
		},
		{
			// package 抢占 models 别名而 import 指向别处:重复别名声明,产物不可编译。
			name:    "package_matches_import_mismatch",
			options: fmt.Sprintf(base, `{"column": "users.org_id", "go_type": {"import": "github.com/google/uuid", "package": "models", "type_name": "UUID"}}`),
		},
		{
			name:    "derived_package_mismatch",
			options: derivedBase,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := loadGoldenRequest(t)
			req.PluginOptions = []byte(tc.options)
			_, err := Generate(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), "conflicts with model_import/model_package") {
				t.Fatalf("want model-import conflict error, got: %v", err)
			}
		})
	}

	t.Run("model_package_without_model_import_rejected", func(t *testing.T) {
		// 仅设 model_package(无 model_import)是被 qualify 忽略的死配置:
		// 模型引用不带前缀而 override 的显式 package 前缀仍会进产物,引用
		// 悬空——生成期显式拒绝,而非静默产出不可编译的产物。
		req := loadGoldenRequest(t)
		setComment(req, "users", "balance", "[@updatable: set]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"model_package": "models",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.balance", "go_type": {"import": "github.com/shopspring/decimal", "package": "models", "type_name": "Decimal"}}
			]
		}`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "model_package is set but model_import is empty") {
			t.Fatalf("want dead model_package error, got: %v", err)
		}
	})
}

// TestGoTypeOverrideAliasConflict 覆盖 override 之间的 import / 别名冲突
// 前置校验(与 model_import 同族故障:不拦截则错误落在产物编译期):
//   - 声明级(ParseOptions):同一 import 路径声明不同别名 → fail(按路径
//     收集的映射会让其中一列的类型前缀静默悬空);
//   - 发射级(importsSection):同一生成文件内,两个不同路径抢同一别名、
//     或别名撞上该文件的固定 import(sq / dynq / pgx / time / json /
//     context / models)→ fail;别名落在不同表的文件中则各自合法
//     (Go import 别名是文件作用域)。
func TestGoTypeOverrideAliasConflict(t *testing.T) {
	newReq := func(t *testing.T, overrides string) *pb.GenerateRequest {
		t.Helper()
		req := loadGoldenRequest(t)
		req.PluginOptions = []byte(fmt.Sprintf(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [%s]
		}`, overrides))
		return req
	}

	t.Run("same_import_path_conflicting_aliases_rejected", func(t *testing.T) {
		// 同一路径两条声明:显式 package "cash" 与缺省派生 "money" 失配。
		req := newReq(t, `
			{"column": "users.balance", "go_type": {"import": "example.com/money", "package": "cash", "type_name": "Money"}},
			{"column": "users.score", "go_type": {"import": "example.com/money", "type_name": "Money"}}
		`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), `conflicting aliases "cash" and "money"`) {
			t.Fatalf("want conflicting-aliases error, got: %v", err)
		}
	})

	t.Run("same_import_path_consistent_aliases_accepted", func(t *testing.T) {
		req := loadGoldenRequest(t)
		setComment(req, "users", "balance", "[@updatable: set]")
		setComment(req, "users", "score", "[@updatable: set]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.balance", "go_type": {"import": "github.com/shopspring/decimal", "package": "decimal", "type_name": "Decimal"}},
				{"column": "users.score", "go_type": {"import": "github.com/shopspring/decimal", "type_name": "Decimal"}}
			]
		}`)
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		users := mustFile(t, resp, "users_dynamic_gen.go")
		if got := strings.Count(users, `decimal "github.com/shopspring/decimal"`); got != 1 {
			t.Errorf("consistent aliases must emit the import exactly once, got %d", got)
		}
		for _, want := range []string{"decimal.Decimal", "SetBalance", "SetScore"} {
			if !strings.Contains(users, want) {
				t.Errorf("generated surface missing %q", want)
			}
		}
	})

	t.Run("distinct_paths_shared_alias_same_file_rejected", func(t *testing.T) {
		req := newReq(t, `
			{"column": "users.balance", "go_type": {"import": "example.com/a", "package": "x", "type_name": "Decimal"}},
			{"column": "users.org_id", "go_type": {"import": "example.com/b", "package": "x", "type_name": "UUID"}}
		`)
		setComment(req, "users", "balance", "[@updatable: set]")
		setComment(req, "users", "org_id", "[@updatable: set]")
		_, err := Generate(context.Background(), req)
		if err == nil {
			t.Fatal("want shared-alias collision error, got nil")
		}
		for _, want := range []string{
			`alias "x" maps to import "example.com/b"`,
			`already bound to "example.com/a" (column balance)`,
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error missing %q: %v", want, err)
			}
		}
	})

	t.Run("distinct_paths_shared_alias_across_files_accepted", func(t *testing.T) {
		// 同一别名落在不同表的生成文件中:import 别名是文件作用域,各自合法。
		req := newReq(t, `
			{"column": "users.balance", "go_type": {"import": "example.com/a", "package": "shared", "type_name": "Decimal"}},
			{"column": "orders.amount_cents", "go_type": {"import": "example.com/b", "package": "shared", "type_name": "Cents"}}
		`)
		setComment(req, "users", "balance", "[@updatable: set]")
		setComment(req, "orders", "amount_cents", "[@updatable: set]")
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		users := mustFile(t, resp, "users_dynamic_gen.go")
		orders := mustFile(t, resp, "orders_dynamic_gen.go")
		if !strings.Contains(users, `shared "example.com/a"`) || strings.Contains(users, "example.com/b") {
			t.Error("users file must bind the alias to example.com/a only")
		}
		if !strings.Contains(orders, `shared "example.com/b"`) || strings.Contains(orders, "example.com/a") {
			t.Error("orders file must bind the alias to example.com/b only")
		}
	})

	t.Run("override_alias_fixed_import_collision_rejected", func(t *testing.T) {
		req := newReq(t, `
			{"column": "users.balance", "go_type": {"import": "example.com/sqlq", "package": "sq", "type_name": "Builder"}}
		`)
		setComment(req, "users", "balance", "[@updatable: set]")
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), `alias "sq" collides with the generated import "github.com/Masterminds/squirrel"`) {
			t.Fatalf("want fixed-import collision error, got: %v", err)
		}
	})

	t.Run("override_alias_pgx_rejected_in_read_enabled_file", func(t *testing.T) {
		// users 有 @pk(读面启用):文件固定 import pgx,别名 pgx 被占用。
		req := newReq(t, `
			{"column": "users.balance", "go_type": {"import": "example.com/pgxshim", "package": "pgx", "type_name": "Conn"}}
		`)
		setComment(req, "users", "balance", "[@updatable: set]")
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), `alias "pgx" collides with the generated import "github.com/jackc/pgx/v5"`) {
			t.Fatalf("want pgx collision error, got: %v", err)
		}
	})

	t.Run("override_alias_pgx_rejected_in_update_only_file", func(t *testing.T) {
		// 更新面统一产出 RETURNING 入口后,纯更新面文件同样固定 import pgx:
		// 别名 pgx 在任何生成文件中都被占用。碰撞判定仍按文件作用域工作,
		// 不做全局超集拒绝。
		req := newReq(t, `
			{"column": "sys_resource_grant.note", "go_type": {"import": "example.com/pgxshim", "package": "pgx", "type_name": "Conn"}}
		`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), `alias "pgx" collides with the generated import "github.com/jackc/pgx/v5"`) {
			t.Fatalf("want pgx collision error, got: %v", err)
		}
	})
}

// TestGoTypeOverrideDbTypeForm 覆盖 db_type 形态的 override:按 PG 类型名
// 命中全部表的同型列(与 column 形态的 table.column 精确限定相反,跨表是
// db_type 的设计语义)。
func TestGoTypeOverrideDbTypeForm(t *testing.T) {
	req := loadGoldenRequest(t)
	// users.name 与 projects.name 同为 text:一条 db_type 声明同时命中两张表。
	// db_type 会命中范围内全部 text 列(users 的 email/password/role 也是),
	// 一并收敛为 set-only 引用面避开读面分叉拒绝;生成范围收窄到两表,
	// 其余 text 列(varchar 不同名,不入范围)不参与校验与产物。
	setComment(req, "users", "name", "姓名 [@updatable: set]")
	setComment(req, "users", "email", "[@updatable: set]")
	setComment(req, "users", "password", "[@updatable: set]")
	setComment(req, "users", "role", "[@updatable: set]")
	setComment(req, "projects", "name", "项目名 [@updatable: set]")
	req.PluginOptions = []byte(`{
		"package": "gen",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"tables": ["users", "projects"],
		"go_type_overrides": [
			{"db_type": "text", "go_type": {"import": "example.com/x", "package": "x", "type_name": "T"}}
		]
	}`)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	users := mustFile(t, resp, "users_dynamic_gen.go")
	projects := mustFile(t, resp, "projects_dynamic_gen.go")
	for name, src := range map[string]string{"users": users, "projects": projects} {
		if !strings.Contains(src, `x "example.com/x"`) {
			t.Errorf("%s: db_type override import not collected", name)
		}
	}
	if !strings.Contains(users, "func (UserTable) SetName(v x.T) UserUpdateOption") {
		t.Error("users.name must be overridden by db_type text")
	}
	if !strings.Contains(projects, "func (ProjectTable) SetName(v x.T) ProjectUpdateOption") {
		t.Error("projects.name must be overridden by db_type text")
	}
}

// TestGoTypeOverrideDivergence 覆盖 override 与协议门收敛类型的分叉拒绝:
// 读面值 op 的 Params 槽位按 dataType 收敛、时间型 between 边界比较发射
// lo.After(hi)(含 pk 自动矩阵),分叉类型一律生成期 fail;不引用收敛类型
// 的面(模糊三 op)不受限。
func TestGoTypeOverrideDivergence(t *testing.T) {
	t.Run("read_value_op_rejected", func(t *testing.T) {
		req := loadGoldenRequest(t)
		// org_id 保持夹具的 @dynamic: exact, membership(读面值 op),override 为 uuid.UUID。
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.org_id", "go_type": {"import": "github.com/google/uuid", "package": "uuid", "type_name": "UUID"}}
			]
		}`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), `go_type_overrides type "uuid.UUID" diverges from the uuid coercion type`) {
			t.Fatalf("want divergence error, got: %v", err)
		}
	})

	t.Run("read_value_op_update_side_allowed", func(t *testing.T) {
		// 同一分叉列仅保留更新面引用(Set / Where 值透传 any):生成成功。
		req := loadGoldenRequest(t)
		setComment(req, "users", "org_id", "[@updatable: eq]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.org_id", "go_type": {"import": "github.com/google/uuid", "package": "uuid", "type_name": "UUID"}}
			]
		}`)
		if _, err := Generate(context.Background(), req); err != nil {
			t.Fatalf("update-side reference must be allowed: %v", err)
		}
	})

	t.Run("datetime_between_update_rejected", func(t *testing.T) {
		req := loadGoldenRequest(t)
		setComment(req, "users", "updated_at", "更新时间 [@updatable: between]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.updated_at", "go_type": {"import": "example.com/x", "package": "x", "type_name": "T"}}
			]
		}`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "emits lo.After(hi)") {
			t.Fatalf("want between divergence error, got: %v", err)
		}
	})

	t.Run("numeric_between_update_rejected", func(t *testing.T) {
		// 数值型 between 发射 lo > hi:对 struct/指针型 override 同样不可编译,
		// 与时间型一并拒绝(有序命名类型的例外场景过窄,不做白名单)。
		req := loadGoldenRequest(t)
		setComment(req, "users", "age", "年龄 [@updatable: between]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.age", "go_type": {"import": "example.com/x", "package": "x", "type_name": "T"}}
			]
		}`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "emits lo > hi") {
			t.Fatalf("want numeric between divergence error, got: %v", err)
		}
	})

	t.Run("no_pk_update_only_between_rejected", func(t *testing.T) {
		// 无 @pk 纯更新面表不经 validateTable 主流程(提前返回):between 分叉
		// 检测须在该分支同样生效(更新面正是 between 的主要形态)。
		req := loadGoldenRequest(t)
		users := findTable(req, "users")
		for _, c := range users.Columns {
			c.Comment = ""
		}
		setComment(req, "users", "name", "姓名 [@updatable: set]")
		setComment(req, "users", "updated_at", "更新时间 [@updatable: between]")
		users.Comment = "用户表"
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.updated_at", "go_type": {"import": "example.com/x", "package": "x", "type_name": "T"}}
			]
		}`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "emits lo.After(hi)") {
			t.Fatalf("want no-pk between divergence error, got: %v", err)
		}
	})

	t.Run("pk_auto_matrix_between_rejected", func(t *testing.T) {
		// 时间型主键 + 分叉 override:pk 自动定位矩阵含 between,同样发射
		// lo.After(hi),须拒绝(pk 不声明 @updatable,定位矩阵由 @pk 自动授予)。
		req := loadGoldenRequest(t)
		users := findTable(req, "users")
		for _, c := range users.Columns {
			c.Comment = ""
			if c.Name == "id" {
				c.Type = &pb.Identifier{Name: "timestamptz"}
			}
		}
		setComment(req, "users", "name", "姓名 [@updatable: set]")
		users.Comment = "用户表 [@pk: id]"
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.id", "go_type": {"import": "example.com/x", "package": "x", "type_name": "T"}}
			]
		}`)
		_, err := Generate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "emits lo.After(hi)") {
			t.Fatalf("want pk between divergence error, got: %v", err)
		}
	})

	t.Run("identical_type_allowed", func(t *testing.T) {
		// 与默认映射一致的 override(如 uuid → string)不构成分叉,照常放行。
		req := loadGoldenRequest(t)
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.org_id", "go_type": {"type_name": "string"}}
			]
		}`)
		if _, err := Generate(context.Background(), req); err != nil {
			t.Fatalf("identical override type must be allowed: %v", err)
		}
	})

	t.Run("fuzzy_only_read_allowed", func(t *testing.T) {
		// 模糊三 op 的 Params 槽位恒 string,不引用列 GoType:分叉 override
		// 可用,且外部包不被收集(零引用)。
		req := loadGoldenRequest(t)
		setComment(req, "users", "name", "姓名 [@dynamic: fuzzy]")
		req.PluginOptions = []byte(`{
			"package": "gen",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
			"go_type_overrides": [
				{"column": "users.name", "go_type": {"import": "github.com/google/civil", "package": "civil", "type_name": "Date"}}
			]
		}`)
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("fuzzy-only read with divergent override must be allowed: %v", err)
		}
		users := mustFile(t, resp, "users_dynamic_gen.go")
		if strings.Contains(users, "civil") {
			t.Error("fuzzy-only slots do not reference the override type; import must be skipped")
		}
	})
}

// TestTypedOptionValue 覆盖 options.ui 静态字典值的类型定型:integer → int64,
// string/uuid → string,其他 dataType 拒绝;禁止 float64 中转。
func TestTypedOptionValue(t *testing.T) {
	cases := []struct {
		name     string
		dataType string
		raw      string
		want     any
		wantErr  string
	}{
		{"integer", "integer", "0", int64(0), ""},
		{"negative_integer", "integer", "-3", int64(-3), ""},
		{"integer_float_rejected", "integer", "1.5", nil, "not a valid integer"},
		{"integer_string_rejected", "integer", `"1"`, nil, "must be an integer"},
		{"string", "string", `"admin"`, "admin", ""},
		{"uuid", "uuid", `"11111111-1111-1111-1111-111111111111"`, "11111111-1111-1111-1111-111111111111", ""},
		{"enum_unsupported", "enum", `"active"`, nil, "options not supported for dataType enum"},
		{"jsonb_unsupported", "jsonb", `"k"`, nil, "options not supported for dataType jsonb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := typedOptionValue(tc.dataType, json.RawMessage(tc.raw))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %#v (%T), want %#v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

// TestCommentBracketBalance 覆盖注释层方括号配平(引号豁免)与人类文本不参与
// 生成的解析规则。
func TestCommentBracketBalance(t *testing.T) {
	// 非 @ 块是纯文档文本,原样跳过;成对方括号合法。
	tags, err := parseComment("column t.c", "余额 [元] 说明 [已废弃，仅展示] [@dynamic: range]")
	if err != nil {
		t.Fatalf("documentation brackets must be legal: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "dynamic" || tags[0].Payload != "range" {
		t.Fatalf("want only @dynamic: range, got %+v", tags)
	}
	// 单引号字符串内部的方括号不参与配平,也不终止指令块。
	tags, err = parseComment("table t", "表 [@filter: name eq 'a]b' @pk: id]")
	if err != nil {
		t.Fatalf("brackets inside quotes must be ignored: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("want @filter and @pk, got %+v", tags)
	}
	// 裸 ] → fail。
	if _, err := parseComment("column t.c", "x ] y"); err == nil || !strings.Contains(err.Error(), "unmatched ']'") {
		t.Fatalf("bare ']' must fail: %v", err)
	}
	// 未闭合 [ → fail;未闭合引号按普通字符处理。
	if _, err := parseComment("column t.c", "x [y"); err == nil || !strings.Contains(err.Error(), "unclosed '['") {
		t.Fatalf("unclosed '[' must fail: %v", err)
	}
	if _, err := parseComment("column t.c", "it's unbalanced [y"); err == nil || !strings.Contains(err.Error(), "unclosed '['") {
		t.Fatalf("unclosed quote must not swallow later brackets: %v", err)
	}
	// 块外人类文本的撇号不得把裸 ] 当成字符串内容而漏检。
	if _, err := parseComment("column t.c", "don't ] can't"); err == nil || !strings.Contains(err.Error(), "unmatched ']'") {
		t.Fatalf("apostrophes in human text must not hide a bare ']': %v", err)
	}
	// 块外撇号 + 块内字面量:撇号不能被当作字面量开引号而误报块内 ]。
	tags, err = parseComment("table t", "it's [@filter: name eq 'a]b' @pk: id]")
	if err != nil {
		t.Fatalf("apostrophe before a directive must not break quoted literals: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("want @filter and @pk after apostrophe prefix, got %+v", tags)
	}
}

// TestUIManifestInjection 覆盖 options.ui 注入:label / order / visibility /
// options / api 落到 registry;未声明 label 的列回落物理列名(注释人类文本
// 不再参与生成)。
func TestUIManifestInjection(t *testing.T) {
	req := loadGoldenRequest(t)
	req.PluginOptions = []byte(`{
		"package": "gen",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"ui": {
			"users": {
				"name": {"label": "姓名UI", "order": 5, "visibility": "hidden"},
				"age": {"options": [{"value": 0, "label": "未知"}, {"value": 18, "label": "成年"}]},
				"status": {"desc": "账号状态", "visibility": "always"}
			},
			"orders": {
				"user_id": {"api": {"endpoint": "/api/v1/users/options", "value_key": "id", "label_key": "name", "mode": "search"}}
			}
		}
	}`)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	users := mustFile(t, resp, "users_dynamic_gen.go")
	// gofmt 会按元素内的最长键对齐,断言按"同一行包含"匹配而非固定空格。
	hasLine := func(parts ...string) bool {
		for _, line := range strings.FieldsFunc(users, func(r rune) bool { return r == '\n' }) {
			ok := true
			for _, p := range parts {
				if !strings.Contains(line, p) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		name  string
		parts []string
	}{
		{"label", []string{"Label:", `"姓名UI",`}},
		{"order", []string{"Order:", "dynq.IntPtr(5),"}},
		{"hidden", []string{"Visibility:", "dynq.VisibilityHidden,"}},
		{"always", []string{"Visibility:", "dynq.VisibilityAlways,"}},
		{"desc", []string{"Desc:", `"账号状态",`}},
		// 未在 manifest 声明的列同样显式发射 optional(不依赖零值隐式)。
		{"optional_explicit", []string{"Visibility:", "dynq.VisibilityOptional,"}},
		{"option0", []string{`{Value: int64(0), Label: "未知"},`}},
		{"option18", []string{`{Value: int64(18), Label: "成年"},`}},
		// 未声明 UI label 的列回落物理列名,注释人类文本不参与生成。
		{"label_fallback", []string{"Label:", `"email",`}},
	} {
		if !hasLine(tc.parts...) {
			t.Errorf("users UI manifest surface missing %s (%v)", tc.name, tc.parts)
		}
	}
	if strings.Contains(users, `Label:      "电子邮箱"`) {
		t.Error("comment human text must not become a label")
	}
	orders := mustFile(t, resp, "orders_dynamic_gen.go")
	for _, want := range []string{
		`API: &dynq.FieldAPI{`,
		`Endpoint: "/api/v1/users/options",`,
		`ValueKey: "id",`,
		`LabelKey: "name",`,
		`Mode:     "search",`,
	} {
		if !strings.Contains(orders, want) {
			t.Errorf("orders UI api surface missing %q", want)
		}
	}
}

// TestPagePolicyFromOptions 覆盖 options.page:全局值打底、表级覆盖优先,
// 文件头不再出现 @page。
func TestPagePolicyFromOptions(t *testing.T) {
	req := loadGoldenRequest(t)
	req.PluginOptions = []byte(`{
		"package": "gen",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"page": {"default": 30, "max": 300, "tables": {"orders": {"max": 100}}}
	}`)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	users := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{"PageDefault: 30,", "PageMax:     300,"} {
		if !strings.Contains(users, want) {
			t.Errorf("users page policy missing %q", want)
		}
	}
	orders := mustFile(t, resp, "orders_dynamic_gen.go")
	for _, want := range []string{"PageDefault: 30,", "PageMax:     100,"} {
		if !strings.Contains(orders, want) {
			t.Errorf("orders page override missing %q", want)
		}
	}
	if strings.Contains(users, "@page") {
		t.Error("header must not echo @page")
	}
}

// TestDynamicSortToken 覆盖 @dynamic 的 sort 能力位:只置 Sortable、不生成
// Params 字段;纯 sort 声明仍应用 default_ops 保留过滤能力。
func TestDynamicSortToken(t *testing.T) {
	req := loadGoldenRequest(t)
	setComment(req, "users", "name", "姓名 [@dynamic: sort]")
	req.PluginOptions = []byte(`{
		"package": "gen",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"default_ops": "exact",
		"tables": ["users"]
	}`)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	// sort 只置 Sortable:游标取值覆盖 name。
	wantCursorCase := `case "name":` + "\n\t\t\tvals[i] = row.Name"
	if !strings.Contains(s, wantCursorCase) {
		t.Error("@dynamic: sort must make the column sortable")
	}
	// default_ops 回落:sort 之外无 op 项时仍授予过滤能力。
	if !strings.Contains(s, "NameNeq") {
		t.Error("@dynamic: sort must still apply default_ops filtering")
	}
	// sort 是能力位,不生成 Params 字段。
	if strings.Contains(s, "NameSort") {
		t.Error("@dynamic: sort must not generate a Params field")
	}
	if !strings.Contains(s, "column users.name: [@dynamic: sort]") {
		t.Error("header echo must list the sort capability token")
	}
}

// TestDynamicPresetMembership 锁定预设词表:membership 展开 in+not_in;
// 裸 in 是普通 op(只 in),不再隐含 not_in;sort 是独立能力位。
func TestDynamicPresetMembership(t *testing.T) {
	ops, sortable, err := expandDynamicItems("column t.c", "string", "in", false)
	if err != nil {
		t.Fatalf("bare in: %v", err)
	}
	if sortable || len(ops) != 1 || ops[0] != "in" {
		t.Fatalf("bare in must mean only the in op, got ops=%v sortable=%v", ops, sortable)
	}
	ops, sortable, err = expandDynamicItems("column t.c", "string", "membership", false)
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	if sortable || len(ops) != 2 || ops[0] != "in" || ops[1] != "not_in" {
		t.Fatalf("membership must expand to in+not_in, got ops=%v sortable=%v", ops, sortable)
	}
	ops, sortable, err = expandDynamicItems("column t.c", "string", "sort", false)
	if err != nil {
		t.Fatalf("sort: %v", err)
	}
	if !sortable || len(ops) != 0 {
		t.Fatalf("sort is a capability token, got ops=%v sortable=%v", ops, sortable)
	}
}

// TestImplicitSortable 锁定隐式排序能力:@default_sort 目标列与 @pk 列即使
// 没有任何列级指令也进入可排序列(registry Sortable + 游标取值)。
func TestImplicitSortable(t *testing.T) {
	req := loadGoldenRequest(t)
	users := findTable(req, "users")
	for _, c := range users.Columns {
		c.Comment = ""
	}
	users.Comment = "用户表 [@default_sort: email DESC @pk: id]"
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, key := range []string{"email", "id"} {
		if !registryFieldTrue(s, key, "Sortable") {
			t.Errorf("%s must be implicitly sortable from @default_sort / @pk", key)
		}
	}
	if !strings.Contains(s, `case "email":`) {
		t.Error("@default_sort target must be included in cursor values")
	}
}

// registryFieldTrue 在生成源码的 registry 字面量中定位 Key==key 的元素,
// 检查其 field 是否为 true(规避 gofmt 对齐宽度差异)。
func registryFieldTrue(src, key, field string) bool {
	lines := strings.FieldsFunc(src, func(r rune) bool { return r == '\n' })
	in := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Key:") && strings.Contains(t, `"`+key+`"`) {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(t, "},") {
			in = false
			continue
		}
		if strings.HasPrefix(t, field+":") && strings.Contains(t, "true") {
			return true
		}
	}
	return false
}
