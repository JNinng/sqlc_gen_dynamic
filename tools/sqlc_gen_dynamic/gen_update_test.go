package main

import (
	"context"
	"strings"
	"testing"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// gen_update_test.go:动态更新面(typed 门)产物测试 —— SET / WHERE 能力切分、
// @pk 自动定位、各 op 分支的 SQL 与守卫、@filter 绕过、执行函数与跨包
// model_import。读面与 DSL 失败路径分别见 golden_test.go / dsl_test.go。

// TestGeneratePkAutoLocator 覆盖 A4 的两条正向规则:
//  1. 有 @pk、只有 set-capable 列(无任何 where 声明)也能生成更新面——
//     where 资格完全由 pk 自动矩阵满足;
//  2. 显式 @updatable: auto 与裸形态等价(Set + Where),策略头回显裸形态。
func TestGeneratePkAutoLocator(t *testing.T) {
	// 只有 set-capable:pk 必须是唯一的定位来源。
	req := loadGoldenRequest(t)
	users := findTable(req, "users")
	for _, c := range users.Columns {
		c.Comment = ""
	}
	setComment(req, "users", "name", "姓名 [@updatable: set]")
	setComment(req, "users", "score", "评分 [@updatable: set]")
	users.Comment = "用户表 [@pk: id]"
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate(set-only + pk): %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		"func (UserTable) SetName(v string) UserUpdateOption",
		"func (UserTable) SetScore(v *float64) UserUpdateOption",
		"func (UserTable) WhereIDEq(v int64) UserUpdateOption",
		"func (UserTable) WhereIDBetween(lo, hi int64) UserUpdateOption",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("pk auto locator surface missing %q", want)
		}
	}
	if strings.Contains(s, "WhereNameEq") || strings.Contains(s, "WhereScoreGt") {
		t.Error("set-only columns must not generate WHERE methods")
	}
	if strings.Contains(s, "SetID") {
		t.Error("@pk column must never generate SET")
	}

	// 显式 auto:Set + WHERE 两侧齐全(与裸形态等价);策略头回显裸形态。
	req = loadGoldenRequest(t)
	users = findTable(req, "users")
	for _, c := range users.Columns {
		c.Comment = ""
	}
	setComment(req, "users", "email", "邮箱 [@updatable: auto]")
	users.Comment = "用户表 [@pk: id]"
	resp, err = Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate(explicit auto): %v", err)
	}
	s = mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		"func (UserTable) SetEmail(v string) UserUpdateOption",
		"func (UserTable) WhereEmailEq(v string) UserUpdateOption",
		"column users.email: [@updatable]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("explicit auto surface missing %q", want)
		}
	}
}

// TestGenerateUpdateSurface 覆盖动态更新面产物:五形态下的 SET / WHERE
// 能力切分(@internal 正交、set-only、where-only、false 惰性、显式清单)、
// @pk 自动定位矩阵、@filter 绕过与 Update 不消费 @scope。
func TestGenerateUpdateSurface(t *testing.T) {
	req := loadGoldenRequest(t)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	src := map[string]string{}
	for _, f := range resp.Files {
		src[f.Name] = string(f.Contents)
	}
	users := src["users_dynamic_gen.go"]
	for _, want := range []string{
		"type UserTable struct{}",
		"var UserUpdate UserTable",
		"type UserUpdateOption = dynq.UpdateOption[UserTable]",
		// 非空列:值类型;可空列:*T(nil = 显式置 NULL)。
		"func (UserTable) SetName(v string) UserUpdateOption",
		"func (UserTable) SetRole(v *string) UserUpdateOption",
		// Set 注释:列 label 紧随列名;非空列标注无 nil,可空列说明 dynq.Ptr。
		"// SetName 设置 name（姓名）列值，无 nil。",
		"// SetRole 设置 role（角色）列值，nil = 显式置 NULL，字面量取指针用 dynq.Ptr。",
		// @internal + set:服务端可写、协议不可见(Update 与 @internal 正交)。
		"func (UserTable) SetPassword(v string) UserUpdateOption",
		"func (UserTable) WherePasswordEq(v string) UserUpdateOption",
		// 类型 cast:枚举 / int[] / jsonb。
		`st.Set("status", "status", "::user_status", v)`,
		`st.Set("tags", "tags", "::int4[]", v)`,
		`st.Set("meta", "meta", "::jsonb", v)`,
		// @pk 自动定位矩阵:无需 id 声明 @updatable。
		"func (UserTable) WhereIDEq(v int64) UserUpdateOption",
		"func (UserTable) WhereIDIn(vs ...int64) UserUpdateOption",
		"func (UserTable) WhereIDBetween(lo, hi int64) UserUpdateOption",
		// auto / 显式清单 / where-only 的 WHERE 能力。
		"func (UserTable) WhereNameEq(v string) UserUpdateOption",
		"func (UserTable) WhereStatusIn(vs ...UserStatus) UserUpdateOption",
		"func (UserTable) WhereRoleEq(v string) UserUpdateOption",         // exact 预设:set + where
		"func (UserTable) WhereAgeBetween(lo, hi int64) UserUpdateOption", // 显式清单 eq, range
		"func (UserTable) WhereOrgIDEq(v string) UserUpdateOption",        // where-only
		"func (UserTable) WhereTagsIn(vs []int64) UserUpdateOption",
		"func (UserTable) WhereUpdatedAtIsNull() UserUpdateOption",
		// 执行函数:typed 选项 + @filter 不变量(可绕过)+ execrows。
		"func UpdateUsers(ctx context.Context, db DBTX, opts ...UserUpdateOption) (int64, error)",
		"func (UserTable) WithoutFilter() UserUpdateOption",
		"if !st.FilterBypassed() {",
		`conds = append(conds, sq.Expr("deleted_at IS NULL"))`,
		`b = b.Set(a.Column, sq.Expr("?"+a.Cast, a.Value))`,
		"return tag.RowsAffected(), nil",
	} {
		if !strings.Contains(users, want) {
			t.Errorf("users update surface missing %q", want)
		}
	}
	// 能力切分:set-only / false 不产 WHERE;where-only 不产 Set;
	// 显式清单外的 op 不产方法。
	for _, forbidden := range []string{
		"WhereEmail", "WhereScore", "WhereBalance", "WhereMeta", "WhereDeletedAt",
		"WhereBirthDate", "SetBirthDate", "SetOrgID", "WhereRoleIn", "WhereAgeIn",
		"WhereUpdatedAtGt", "SetID", `st.Set("createdAt"`,
	} {
		if strings.Contains(users, forbidden) {
			t.Errorf("unexpected update symbol %q", forbidden)
		}
	}
	// Update 不消费 @scope:即便表声明了 @scope 也不取 ScopeFromContext。
	for _, name := range []string{"users", "customers", "projects"} {
		body := updateFuncBody(src[name+"_dynamic_gen.go"], "Update"+goName(name))
		if body == "" {
			t.Fatalf("%s: Update function missing", name)
		}
		if strings.Contains(body, "ScopeFromContext") {
			t.Errorf("%s: Update must not consume @scope", name)
		}
	}

	// @scope 表 + @filter:显式 @update: true 回显;filter 仍生效,无权限谓词。
	customers := src["customers_dynamic_gen.go"]
	for _, want := range []string{
		"table customers: [@update: true]",
		"func (CustomerTable) SetName(v string) CustomerUpdateOption",
		"func (CustomerTable) SetAmountCents(v int64) CustomerUpdateOption",
		"func (CustomerTable) WhereIDEq(v int64) CustomerUpdateOption",
	} {
		if !strings.Contains(customers, want) {
			t.Errorf("customers update surface missing %q", want)
		}
	}

	// @filter 不变量进 UPDATE(projects),WithoutFilter 生成;无 scope 谓词。
	projects := src["projects_dynamic_gen.go"]
	for _, want := range []string{
		"func (ProjectTable) WithoutFilter() ProjectUpdateOption",
		`conds = append(conds, sq.Expr("name = ?", "template"))`,
	} {
		if !strings.Contains(projects, want) {
			t.Errorf("projects update surface missing %q", want)
		}
	}
	if strings.Contains(updateFuncBody(projects, "UpdateProjects"), "fn_tenant_id") {
		t.Error("projects: Update must not inline @scope guard predicates")
	}

	// 枚举显式清单(status: eq, neq)带 cast;user_id 为 where-only 不产 Set。
	orders := src["orders_dynamic_gen.go"]
	for _, want := range []string{
		`st.Set("status", "status", "::order_status", v)`,
		`sq.Expr("status = ?::order_status", v)`,
		"func (OrderTable) WhereUserIDEq(v int64) OrderUpdateOption",
	} {
		if !strings.Contains(orders, want) {
			t.Errorf("orders update surface missing %q", want)
		}
	}
	if strings.Contains(orders, "SetUserID") {
		t.Error("orders: where-only column must not generate SET")
	}
}

// TestGenerateSetCommentForms 覆盖 Set 注释的 label 抑制:注释无人类 label
// (或 label 与物理列名相同)时,不再重复输出物理名。
func TestGenerateSetCommentForms(t *testing.T) {
	req := loadGoldenRequest(t)
	setComment(req, "users", "name", "[@updatable: set]")
	setComment(req, "users", "email", "email [@updatable: set]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		"// SetName 设置 name 列值，无 nil。",
		"// SetEmail 设置 email 列值，无 nil。",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("label-less Set comment missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"// SetName 设置 name（",
		"// SetEmail 设置 email（",
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("default label must be suppressed, got %q", forbidden)
		}
	}
}

// updateFuncBody 截取生成文件中某个 UpdateXxx 函数体(用于局部断言,
// 避免与读侧注释/代码的同名符号混淆)。
func updateFuncBody(src, fn string) string {
	i := strings.Index(src, "func "+fn+"(")
	if i < 0 {
		return ""
	}
	if j := strings.Index(src[i:], "\n}\n"); j >= 0 {
		return src[i : i+j+3]
	}
	return src[i:]
}

// TestGenerateNoPkUpdateOnly 覆盖无 @pk 表的"无读面更新面":只含 marker /
// Set / Where / UpdateXxx,不生成 registry / Params / Parse / List / Count /
// Schema / 游标,且 import 不含 pgx。
func TestGenerateNoPkUpdateOnly(t *testing.T) {
	req := loadGoldenRequest(t)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var s = mustFile(t, resp, "sys_resource_grant_dynamic_gen.go")
	for _, want := range []string{
		"type SysResourceGrantTable struct{}",
		"var SysResourceGrantUpdate SysResourceGrantTable",
		"func (SysResourceGrantTable) SetNote(v *string) SysResourceGrantUpdateOption",
		"func (SysResourceGrantTable) WhereResourceEq(v string) SysResourceGrantUpdateOption",
		"func (SysResourceGrantTable) WhereRecordIDBetween(lo, hi int64) SysResourceGrantUpdateOption",
		"func UpdateSysResourceGrant(ctx context.Context, db DBTX, opts ...SysResourceGrantUpdateOption) (int64, error)",
		`sq.Update("sys_resource_grant")`,
		"本表无 @pk",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no-pk update surface missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"Registry", "FieldIndex", "ListSysResourceGrant", "ParseSysResourceGrant",
		"CountSysResourceGrant", "SysResourceGrantQuerySchema", "CursorValues",
		`"github.com/jackc/pgx/v5"`,
	} {
		if strings.Contains(s, forbidden) {
			t.Errorf("no-pk update surface must not contain %q", forbidden)
		}
	}
}

// TestGenerateUpdateBareDirective 覆盖裸 @update(= @update: true)与策略头
// 原文回显。
func TestGenerateUpdateBareDirective(t *testing.T) {
	req := loadGoldenRequest(t)
	setTableComment(req, "users", "用户表 [@default_sort: created_at DESC @pk: id @page: 20/200 @filter: deleted_at is_null @update]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	if !strings.Contains(s, "table users: [@update]") {
		t.Error("bare @update echo missing")
	}
	if !strings.Contains(s, "func UpdateUsers(") {
		t.Error("bare @update must enable the update surface")
	}
}

// TestGeneratePkUpdatableInert 覆盖 @pk 列上的 where / false 形态:惰性(不
// 改生成结果),pk 仍自动获得完整类型矩阵定位方法,且永不生成 Set。
func TestGeneratePkUpdatableInert(t *testing.T) {
	for _, payload := range []string{"where", "false"} {
		req := loadGoldenRequest(t)
		setComment(req, "users", "id", "ID [@dynamic: exact, in @sortable @hideable @updatable: "+payload+"]")
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("@updatable: %s on pk must be inert: %v", payload, err)
		}
		s := mustFile(t, resp, "users_dynamic_gen.go")
		if !strings.Contains(s, "func (UserTable) WhereIDEq(v int64) UserUpdateOption") {
			t.Errorf("@updatable: %s on pk must keep the auto where matrix", payload)
		}
		if strings.Contains(s, "SetID") {
			t.Errorf("@updatable: %s on pk must never generate SetID", payload)
		}
	}
}

// TestGenerateUpdatableFalseUnmapped 覆盖 @updatable: false 的惰性:不可映射
// 类型不参与任何判定,生成期放行;该列仍是正常读面列,但不产 Set/Where。
func TestGenerateUpdatableFalseUnmapped(t *testing.T) {
	req := loadGoldenRequest(t)
	addColumn(req, "users", "blob_col", "bytea", true)
	setComment(req, "users", "blob_col", "载荷 [@updatable: false]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("@updatable: false must not require a mappable type: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	if !strings.Contains(s, `Column:     "blob_col"`) {
		t.Error("@updatable: false column must stay in the read-side registry")
	}
	if strings.Contains(s, "SetBlobCol") || strings.Contains(s, "WhereBlobCol") {
		t.Error("@updatable: false column must not generate Set/Where methods")
	}
}

// TestGenerateUpdateFalseWithLazyDeclarations 覆盖 @update: false 与惰性声明
// 共存的合法组合:列上 @updatable: false、pk 上 @updatable: where 均为惰性,
// 只读面保留、不生成更新面。
func TestGenerateUpdateFalseWithLazyDeclarations(t *testing.T) {
	req := loadGoldenRequest(t)
	users := findTable(req, "users")
	for _, c := range users.Columns {
		c.Comment = ""
	}
	setComment(req, "users", "name", "姓名 [@updatable: false]")
	setComment(req, "users", "id", "ID [@updatable: where]")
	users.Comment = "用户表 [@pk: id @update: false]"
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("@update: false + lazy declarations must be legal: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	if !strings.Contains(s, "func ListUsers(") {
		t.Error("read surface must be kept with @update: false")
	}
	for _, forbidden := range []string{"UserTable", "UserUpdate", "UpdateUsers"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("@update: false must not emit %q", forbidden)
		}
	}
}

// TestGenerateNoPkFilter 覆盖无 @pk 更新面表的 @filter:不变量进 UPDATE,
// WithoutFilter 生成(读面仍为零产物)。
func TestGenerateNoPkFilter(t *testing.T) {
	req := loadGoldenRequest(t)
	setTableComment(req, "sys_resource_grant", "共享授权 [@filter: note is_null]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "sys_resource_grant_dynamic_gen.go")
	for _, want := range []string{
		"func (SysResourceGrantTable) WithoutFilter() SysResourceGrantUpdateOption",
		"if !st.FilterBypassed() {",
		`conds = append(conds, sq.Expr("note IS NULL"))`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no-pk update surface missing %q", want)
		}
	}
}

// TestGenerateUpdateDisabled 覆盖 @update: false 显式退出:即使表本可有更新面
// (此处列指令被剥离,只剩 @pk),产物也不含任何 Update 面符号。
func TestGenerateUpdateDisabled(t *testing.T) {
	req := loadGoldenRequest(t)
	users := findTable(req, "users")
	for _, c := range users.Columns {
		c.Comment = ""
	}
	users.Comment = "用户表 [@pk: id @update: false]"
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, forbidden := range []string{"UserTable", "UserUpdate", "UpdateUsers"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("@update: false must not emit %q", forbidden)
		}
	}
}

// TestGenerateUpdateEchoForms 覆盖 @update 各载荷的策略头原文回显:
// 裸形态回显 @update,true / auto / false 回显声明原文;false 不产更新面。
func TestGenerateUpdateEchoForms(t *testing.T) {
	base := "用户表 [@default_sort: created_at DESC @pk: id @page: 20/200 @filter: deleted_at is_null "
	for _, tc := range []struct{ name, payload, echo string }{
		{"bare", "@update", "[@update]"},
		{"true", "@update: true", "[@update: true]"},
		{"auto", "@update: auto", "[@update: auto]"},
	} {
		req := loadGoldenRequest(t)
		setTableComment(req, "users", base+tc.payload+"]")
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: Generate: %v", tc.name, err)
		}
		s := mustFile(t, resp, "users_dynamic_gen.go")
		if !strings.Contains(s, "table users: "+tc.echo) {
			t.Errorf("%s: strategy echo missing %q", tc.name, tc.echo)
		}
		if !strings.Contains(s, "func UpdateUsers(") {
			t.Errorf("%s: update surface must be enabled", tc.name)
		}
	}

	// false:剥离列指令后合法;回显原文且不产更新面。
	req := loadGoldenRequest(t)
	users := findTable(req, "users")
	for _, c := range users.Columns {
		c.Comment = ""
	}
	users.Comment = "用户表 [@pk: id @update: false]"
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("false: Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	if !strings.Contains(s, "table users: [@update: false]") {
		t.Error("false: strategy echo missing")
	}
	if strings.Contains(s, "UpdateUsers") {
		t.Error("false: must not generate an update surface")
	}
}

// TestGenerateUpdateOpFamily 覆盖更新面各 op 分支的生成(SQL/签名/守卫),
// 包括 datetime between、not_null、jsonb has(左列 cast + 空 JSON 拒绝)、
// uuid SET cast、int_array && 预设交集、同列重复取首条、显式清单去重排序、
// auto 不授予 null op、空串模糊定位拒绝。
func TestGenerateUpdateOpFamily(t *testing.T) {
	cases := []struct {
		name    string
		column  string
		comment string
		want    []string
		forbid  []string
	}{
		{
			name: "datetime_between_not_null", column: "updated_at",
			comment: "更新时间 [@dynamic: range @updatable: between, not_null]",
			want: []string{
				"func (UserTable) WhereUpdatedAtBetween(lo, hi time.Time) UserUpdateOption",
				"if lo.After(hi) {",
				"func (UserTable) WhereUpdatedAtNotNull() UserUpdateOption",
				`sq.Expr("updated_at IS NOT NULL")`,
			},
		},
		{
			name: "jsonb_has", column: "meta",
			comment: "元数据 [@dynamic: has @updatable]",
			want: []string{
				"func (UserTable) SetMeta(v *[]byte) UserUpdateOption",
				"func (UserTable) WhereMetaHas(v []byte) UserUpdateOption",
				// 左列显式 cast:json(非 jsonb)列没有 json @> jsonb 运算符。
				`sq.Expr("(meta::jsonb) @> ?::jsonb", string(v))`,
				// 空 JSON 定位值显式拒绝(空切片会生成 @> ''::jsonb 的运行期错误)。
				"if len(v) == 0 {",
				`return dynq.ErrEmptyJSON("meta")`,
			},
		},
		{
			name: "uuid_set_cast", column: "org_id",
			comment: "组织ID [@dynamic: exact, in @updatable]",
			want: []string{
				"func (UserTable) SetOrgID(v *string) UserUpdateOption",
				`st.Set("orgId", "org_id", "::uuid", v)`,
				"func (UserTable) WhereOrgIDEq(v string) UserUpdateOption",
			},
		},
		{
			name: "int_array_preset", column: "tags",
			comment: "标签 [@dynamic: in @updatable: in]",
			want: []string{
				"func (UserTable) WhereTagsIn(vs []int64) UserUpdateOption",
				`sq.Expr("tags && ?::int4[]", vs)`,
				"// WhereTagsIn 追加 tags 列 && 重叠 条件",
			},
			forbid: []string{"WhereTagsNotIn"},
		},
		{
			name: "duplicate_takes_first", column: "role",
			comment: "角色 [@updatable: set @updatable]",
			want: []string{
				"func (UserTable) SetRole(v *string) UserUpdateOption",
			},
			forbid: []string{"WhereRoleEq", "WhereRoleIsNull"},
		},
		{
			name: "canonical_dedupe", column: "age",
			comment: "年龄 [@updatable: in, eq, in]",
			want: []string{
				"@updatable: eq, in, not_in",
				"func (UserTable) WhereAgeEq(v int64) UserUpdateOption",
				"func (UserTable) WhereAgeIn(vs ...int64) UserUpdateOption",
			},
			forbid: []string{"WhereAgeGt"},
		},
		{
			name: "auto_no_null_ops", column: "role",
			comment: "角色 [@updatable]",
			want: []string{
				"func (UserTable) WhereRoleEq(v string) UserUpdateOption",
			},
			forbid: []string{"WhereRoleIsNull", "WhereRoleNotNull"},
		},
		{
			name: "empty_fuzzy_guard", column: "name",
			comment: "姓名 [@dynamic: fuzzy @updatable: contains]",
			want: []string{
				`if v == "" {`,
				`return dynq.ErrEmptyPattern("name")`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := loadGoldenRequest(t)
			setComment(req, "users", tc.column, tc.comment)
			resp, err := Generate(context.Background(), req)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			s := mustFile(t, resp, "users_dynamic_gen.go")
			for _, want := range tc.want {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q", want)
				}
			}
			for _, forbid := range tc.forbid {
				if strings.Contains(s, forbid) {
					t.Errorf("unexpected %q", forbid)
				}
			}
		})
	}
}

// TestGenerateListHasEmptyJSONGuard 覆盖 typed 门 validate 段的空 jsonb
// 守卫:直接构造 Params 传入空非 nil 切片时显式拒绝(U7)。
func TestGenerateListHasEmptyJSONGuard(t *testing.T) {
	req := loadGoldenRequest(t)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		"if params.MetaHas != nil && len(params.MetaHas) == 0 {",
		`return dynq.ErrEmptyJSON("meta")`,
		// List 侧 where 同样左列 cast(U1)。
		`sq.Expr("(meta::jsonb) @> ?::jsonb", string(params.MetaHas))`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("list has-guard missing %q", want)
		}
	}
}

// TestGenerateJSONColumnHas 覆盖 json(非 jsonb)列的 has(U1):PG 没有
// json @> jsonb 运算符,List 与 Update 两侧都必须生成左列 cast 形态。
func TestGenerateJSONColumnHas(t *testing.T) {
	req := loadGoldenRequest(t)
	addColumn(req, "users", "extra", "json", true)
	setComment(req, "users", "extra", "扩展 [@dynamic: has @updatable]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		// List 侧 where 与 Update 侧 Where 方法同形态。
		`sq.Expr("(extra::jsonb) @> ?::jsonb", string(params.ExtraHas))`,
		`sq.Expr("(extra::jsonb) @> ?::jsonb", string(v))`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("json column has surface missing %q", want)
		}
	}
	// 裸 @> 形态不再出现:json 列没有 json @> jsonb 运算符。
	if strings.Contains(s, `sq.Expr("extra @> ?::jsonb"`) {
		t.Error("json column must not emit bare @> form")
	}
}

// TestGenerateNonNilSliceSet 覆盖 NOT NULL 切片列的 nil 运行期守卫:切片
// 类型的 nil 会编码为 NULL,签名无法编译期排除,必须在 Set 选项内拒绝。
func TestGenerateNonNilSliceSet(t *testing.T) {
	req := loadGoldenRequest(t)
	users := findTable(req, "users")
	users.Columns = append(users.Columns, &pb.Column{
		Name:    "codes",
		NotNull: true,
		IsArray: true,
		Type:    &pb.Identifier{Name: "int4"},
	})
	setComment(req, "users", "codes", "编码 [@updatable: set]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		"func (UserTable) SetCodes(v []int64) UserUpdateOption",
		`if v == nil {`,
		`return dynq.ErrNilSet("codes")`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// TestGenerateModelImport 覆盖跨包 model_import:DBTX 与枚举类型带包限定,
// 无 @pk 更新面同样引用 models.DBTX 且不引入 pgx。
func TestGenerateModelImport(t *testing.T) {
	req := loadGoldenRequest(t)
	req.PluginOptions = []byte(`{
		"package": "gen",
		"model_import": "example/db/models",
		"model_package": "models",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq"
	}`)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	users := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		`models "example/db/models"`,
		"func UpdateUsers(ctx context.Context, db models.DBTX, opts ...UserUpdateOption) (int64, error)",
		"func (UserTable) SetStatus(v models.UserStatus) UserUpdateOption",
		"func (UserTable) WhereStatusIn(vs ...models.UserStatus) UserUpdateOption",
	} {
		if !strings.Contains(users, want) {
			t.Errorf("users model-import surface missing %q", want)
		}
	}
	grant := mustFile(t, resp, "sys_resource_grant_dynamic_gen.go")
	if !strings.Contains(grant, "func UpdateSysResourceGrant(ctx context.Context, db models.DBTX") {
		t.Error("no-pk update surface must use models.DBTX in model-import mode")
	}
	if strings.Contains(grant, `"github.com/jackc/pgx/v5"`) {
		t.Error("no-pk update surface must not import pgx")
	}
}

// TestGenerateModelImportOverrideSharedPackage 覆盖 override import 与
// models 同包时不重复声明 import(别名撞名会让产物不可编译)。
func TestGenerateModelImportOverrideSharedPackage(t *testing.T) {
	req := loadGoldenRequest(t)
	// 引用面取更新面 Set 签名(读面值 op 与收敛类型分叉会被生成期拒绝)。
	setComment(req, "users", "org_id", "[@updatable: set]")
	req.PluginOptions = []byte(`{
		"package": "gen",
		"model_import": "example/db/models",
		"model_package": "models",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"go_type_overrides": [
			{"column": "users.org_id", "go_type": {"import": "example/db/models", "package": "models", "type_name": "OrgRef"}}
		]
	}`)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	users := mustFile(t, resp, "users_dynamic_gen.go")
	if got := strings.Count(users, `models "example/db/models"`); got != 1 {
		t.Errorf("models import must appear exactly once, got %d", got)
	}
	if !strings.Contains(users, "func (UserTable) SetOrgID(v *models.OrgRef) UserUpdateOption") {
		t.Error("override referencing the models package must qualify with models.")
	}
}

// TestGenerateMultipleFilters 覆盖单表多条 @filter:全部常量条件都进 UPDATE
// 的 WHERE(防止只发射第一条的回归)。
func TestGenerateMultipleFilters(t *testing.T) {
	req := loadGoldenRequest(t)
	setTableComment(req, "users", "用户表 [@default_sort: created_at DESC @pk: id @page: 20/200 @filter: deleted_at is_null age gte 0]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		`conds = append(conds, sq.Expr("deleted_at IS NULL"))`,
		`conds = append(conds, sq.Expr("age >= ?", 0))`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("multi-filter surface missing %q", want)
		}
	}
}

// TestGenerateMarkerCollision 覆盖更新面包级符号与 sqlc 模型类型的撞名:
// user_table 的模型 UserTable 会撞 users 的 marker,必须在生成期 fail。
func TestGenerateMarkerCollision(t *testing.T) {
	req := loadGoldenRequest(t)
	addPkTable(req, "user_table")
	_, err := Generate(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "collides with an sqlc model type") {
		t.Fatalf("want marker collision error, got: %v", err)
	}
}
