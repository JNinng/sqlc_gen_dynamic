package main

import (
	"context"
	"strings"
	"testing"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// dsl_test.go:注释 DSL 的生成期 fail 路径、弃用名兼容路径与封闭集合锁定。
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
			wantErr: "@internal cannot be combined",
		},
		{
			name:    "hidden_alias_combined_with_dynamic",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@dynamic: exact @hidden]") },
			wantErr: "@internal cannot be combined",
		},
		{
			name:    "internal_combined_with_sortable",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortable @internal]") },
			wantErr: "@internal cannot be combined",
		},
		{
			name:    "unknown_tag",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@sortble]") },
			wantErr: "unknown directive @sortble",
		},
		{
			name:    "unknown_tag_lists_new_names_only",
			mutate:  func(r *pb.GenerateRequest) { setComment(r, "users", "name", "姓名 [@selectablee]") },
			wantErr: "allowed for column: @api, @dynamic, @hideable, @internal, @sortable, @updatable",
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
		// ---- 枚举数组(U2):读能力面生成期拒绝,非法 SQL 不再流到运行期 ----
		{
			name: "enum_array_dynamic_rejected",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					Comment: "状态组 [@dynamic: in]",
				})
			},
			wantErr: `array type "user_status" is not supported for @dynamic/@sortable`,
		},
		{
			name: "enum_array_sortable_rejected",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				users.Columns = append(users.Columns, &pb.Column{
					Name:    "statuses",
					IsArray: true,
					Type:    &pb.Identifier{Name: "user_status"},
					Comment: "状态组 [@sortable]",
				})
			},
			wantErr: `array type "user_status" is not supported for @dynamic/@sortable`,
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
			// U2:策略驱动的拒绝分支——default_ops 命中无指令的枚举数组列同样 fail。
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
			wantErr: `array type "user_status" is not supported for @dynamic/@sortable`,
		},
		{
			name: "default_sort_target_not_sortable",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@default_sort: email DESC @pk: id]")
			},
			wantErr: "must declare @sortable",
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
		// ---- default_ops 死配置(U4):无 @pk 入选表不再被静默忽略 ----
		{
			name: "default_ops_without_pk_rejected",
			mutate: func(r *pb.GenerateRequest) {
				r.PluginOptions = []byte(`{"package":"gen","runtime_import":"github.com/jninng/sqlc_gen_dynamic/dynq","default_ops":"exact"}`)
			},
			wantErr: `default_ops "exact" would grant read-side ops but the table has no @pk`,
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
			name: "api_internal_combined",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "orders", "user_id", "用户ID [@api: /users/options value_key=id label_key=name @internal]")
			},
			wantErr: "@internal cannot be combined",
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
		// ---- @update / @updatable(五形态)----
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
			name: "updatable_list_on_pk_rejected",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "users", "id", "ID [@updatable: eq]")
			},
			wantErr: "must not be set-capable @updatable",
		},
		{
			name:    "update_bad_payload",
			mutate:  func(r *pb.GenerateRequest) { setTableComment(r, "users", "用户表 [@update: yes @pk: id]") },
			wantErr: "@update wants true, false, or auto",
		},
		{
			name: "update_false_conflicts_with_updatable",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@update: false @pk: id]")
			},
			wantErr: "@update: false conflicts with @updatable columns",
		},
		{
			name: "update_true_with_only_false_updatable",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					c.Comment = ""
				}
				setComment(r, "users", "name", "姓名 [@updatable: false]")
				users.Comment = "用户表 [@pk: id @update: true]"
			},
			wantErr: "no column is set-capable",
		},
		{
			name: "update_true_without_set_capable",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					c.Comment = ""
				}
				users.Comment = "用户表 [@update: true @pk: id]"
			},
			wantErr: "no column is set-capable",
		},
		{
			name: "update_auto_without_set_capable",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					c.Comment = ""
				}
				users.Comment = "用户表 [@update: auto @pk: id]"
			},
			wantErr: "no column is set-capable",
		},
		{
			name: "pk_column_set_capable",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "users", "id", "ID [@dynamic: exact, in @sortable @hideable @updatable]")
			},
			wantErr: "must not be set-capable @updatable",
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
			name: "set_capable_without_where_no_pk",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					c.Comment = ""
				}
				setComment(r, "users", "name", "姓名 [@updatable: set]")
				users.Comment = "用户表"
			},
			wantErr: "requires at least one where-capable @updatable",
		},
		{
			name: "where_capable_without_set",
			mutate: func(r *pb.GenerateRequest) {
				users := findTable(r, "users")
				for _, c := range users.Columns {
					c.Comment = ""
				}
				setComment(r, "users", "name", "姓名 [@updatable: where]")
				users.Comment = "用户表 [@pk: id]"
			},
			wantErr: "requires at least one set-capable @updatable",
		},
		{
			name: "nopk_read_directive",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "sys_resource_grant", "resource", "资源 [@dynamic: eq @updatable: where]")
			},
			// 完整上下文文案断言(missing @pk 系列保留一条全文):
			wantErr: "table sys_resource_grant: missing @pk directive in table comment (primary key is required for deterministic pagination)",
		},
		{
			name: "nopk_sortable_directive",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "sys_resource_grant", "note", "备注 [@sortable @updatable: set]")
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
			name: "nopk_hideable_directive",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "sys_resource_grant", "note", "备注 [@hideable @updatable: set]")
			},
			wantErr: "missing @pk",
		},
		{
			name: "nopk_api_directive",
			mutate: func(r *pb.GenerateRequest) {
				setComment(r, "sys_resource_grant", "note", "备注 [@api: /x value_key=v label_key=l @updatable: set]")
			},
			wantErr: "missing @pk",
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
				setComment(r, "sys_resource_grant", "note", "备注 [@sortable @updatable: set]")
				setTableComment(r, "sys_resource_grant", "共享授权 [@default_sort: note ASC]")
			},
			wantErr: "missing @pk",
		},
		{
			name: "nopk_page_directive",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "sys_resource_grant", "共享授权 [@page: 10/20]")
			},
			wantErr: "missing @pk",
		},
		{
			name: "updatable_at_table_level",
			mutate: func(r *pb.GenerateRequest) {
				setTableComment(r, "users", "用户表 [@updatable @pk: id]")
			},
			wantErr: "allowed for table: @default_sort, @filter, @page, @pk, @scope, @update",
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

// TestDeprecatedDirectiveAliases 覆盖指令更名的兼容路径(@hidden → @internal、
// @selectable → @hideable,见 dsl.go deprecatedDirectives):
//   - 旧名 @hidden / @selectable 解析为等价新名,生成物与新名逐字节一致;
//   - 旧名触发 deprecation 提示(按列去重),新名零提示;
//   - 同列新旧名并存时静默合并(别名等价),@hidden 提示至多一条。
func TestDeprecatedDirectiveAliases(t *testing.T) {
	oldReq := loadGoldenRequest(t)
	setComment(oldReq, "users", "password", "密码 [@hidden]")
	setComment(oldReq, "users", "id", "ID [@selectable]")

	newReq := loadGoldenRequest(t)
	setComment(newReq, "users", "password", "密码 [@internal]")
	setComment(newReq, "users", "id", "ID [@hideable]")

	opts, err := ParseOptions(oldReq)
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}

	// 旧名:提示按列去重,内容指向新名与移除版本。
	_, warns, err := buildTableSpecs(oldReq, opts)
	if err != nil {
		t.Fatalf("buildTableSpecs (old names): %v", err)
	}
	wantWarns := []string{
		"column users.id: @selectable is deprecated, use @hideable instead (the old name will be removed in v0.6.0)",
		"column users.password: @hidden is deprecated, use @internal instead (the old name will be removed in v0.6.0)",
	}
	if len(warns) != len(wantWarns) {
		t.Fatalf("want %d deprecations, got %d: %v", len(wantWarns), len(warns), warns)
	}
	for i, w := range wantWarns {
		if warns[i] != w {
			t.Errorf("deprecation[%d]:\n got  %s\n want %s", i, warns[i], w)
		}
	}

	// 新名:零提示。
	if _, warnsNew, err := buildTableSpecs(newReq, opts); err != nil {
		t.Fatalf("buildTableSpecs (new names): %v", err)
	} else if len(warnsNew) != 0 {
		t.Errorf("new names must not trigger deprecations, got %v", warnsNew)
	}

	// 同列新旧名并存:归一后由 mergeTags 静默合并,@hidden 提示至多一条。
	bothReq := loadGoldenRequest(t)
	setComment(bothReq, "users", "password", "密码 [@hidden @internal]")
	if _, warnsBoth, err := buildTableSpecs(bothReq, opts); err != nil {
		t.Fatalf("buildTableSpecs (mixed names): %v", err)
	} else if len(warnsBoth) != 1 || !strings.Contains(warnsBoth[0], "column users.password: @hidden is deprecated") {
		t.Errorf("mixed names want exactly one @hidden deprecation, got %v", warnsBoth)
	}

	// 解析等价:旧名与新名的生成物逐字节一致(回显已归一为新名)。
	oldResp, err := Generate(context.Background(), oldReq)
	if err != nil {
		t.Fatalf("Generate (old names): %v", err)
	}
	newResp, err := Generate(context.Background(), newReq)
	if err != nil {
		t.Fatalf("Generate (new names): %v", err)
	}
	if len(oldResp.Files) == 0 {
		t.Fatal("old/new alias comparison must not pass on empty output")
	}
	if len(oldResp.Files) != len(newResp.Files) {
		t.Fatalf("file count mismatch: %d vs %d", len(oldResp.Files), len(newResp.Files))
	}
	for i := range oldResp.Files {
		if oldResp.Files[i].Name != newResp.Files[i].Name {
			t.Fatalf("file name mismatch: %s vs %s", oldResp.Files[i].Name, newResp.Files[i].Name)
		}
		if string(oldResp.Files[i].Contents) != string(newResp.Files[i].Contents) {
			t.Errorf("%s: old-name and new-name outputs must be byte-identical", oldResp.Files[i].Name)
		}
	}
}

// TestGoTypeOverrideImportCollected 覆盖 U3:带 import 的 go_type_overrides
// 必须把外部包收集进产物 import;且仅在生成面实际引用该列 GoType 时收集——
// 收集面过宽会让 import 悬空(未使用 import 产物不可编译)。
func TestGoTypeOverrideImportCollected(t *testing.T) {
	t.Run("referenced_collected", func(t *testing.T) {
		req := loadGoldenRequest(t)
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
			"*uuid.UUID",
			// Package 缺省取 import 路径末段(shopspring/decimal → decimal)。
			"*decimal.Decimal",
		} {
			if !strings.Contains(users, want) {
				t.Errorf("override import surface missing %q", want)
			}
		}
	})

	t.Run("unreferenced_skipped", func(t *testing.T) {
		req := loadGoldenRequest(t)
		// meta 仅为 where-capable(where-only,矩阵仅 has):Where 方法签名硬编码
		// []byte,不引用 override 类型;birth_date 零能力引用。两者都不得收集。
		setComment(req, "users", "meta", "[@updatable: where]")
		setComment(req, "users", "birth_date", "[@updatable: false]")
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
		// 收集面收窄不得误伤生成面:where-only has 的定位方法照常生成。
		if !strings.Contains(users, "func (UserTable) WhereMetaHas(v []byte) UserUpdateOption") {
			t.Error("where-only has column must keep its Where method")
		}
	})
}
