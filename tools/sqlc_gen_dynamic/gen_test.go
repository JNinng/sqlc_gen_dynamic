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
// GenerateRequest dump(经 DYNQ_DUMP_REQUEST 落盘,见 gen.go 与 sqlc.yaml env),
// 保证与宿主真实形态一致(dump-first 原则)。

const goldenOptions = `{
  "package": "gen",
  "runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
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

// writeRawOnFail 把生成失败的原始源码落盘到测试临时目录,便于排错
// (不写仓库,避免失败测试留下未跟踪文件)。
func writeRawOnFail(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := err.Error()
	if idx := strings.Index(msg, "--- source ---"); idx >= 0 {
		raw := msg[idx+len("--- source ---"):]
		path := filepath.Join(t.TempDir(), "debug_raw.go")
		_ = os.WriteFile(path, []byte(raw), 0o644)
		t.Logf("raw source written to %s", path)
	}
}

// mustFile 返回指定产物内容;文件缺失直接 Fatal——防止按表测试在空循环中
// 空转通过(少生成一个文件必须立刻失败)。
func mustFile(t *testing.T, resp *pb.GenerateResponse, name string) string {
	t.Helper()
	for _, f := range resp.Files {
		if f.Name == name {
			return string(f.Contents)
		}
	}
	t.Fatalf("generated file %s missing (got %v)", name, fileNames(resp))
	return ""
}

func TestGenerateGolden(t *testing.T) {
	req := loadGoldenRequest(t)
	resp, err := Generate(context.Background(), req)
	writeRawOnFail(t, err)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden")
	update := os.Getenv("GOLDEN_UPDATE") == "1"
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
		return
	}
	// 双向文件集合校验:生成的每个文件必须有 golden,每个 golden 文件必须被
	// 生成。仅遍历 resp.Files 会让"整表/整文件消失"的回归静默通过。
	entries, rerr := os.ReadDir(goldenDir)
	if rerr != nil {
		t.Fatalf("read golden dir: %v", rerr)
	}
	goldenSet := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			goldenSet[e.Name()] = true
		}
	}
	genSet := map[string]bool{}
	for _, f := range resp.Files {
		genSet[f.Name] = true
	}
	for name := range goldenSet {
		if !genSet[name] {
			t.Errorf("golden file %s was not generated (table/file dropped?)", name)
		}
	}
	for name := range genSet {
		if !goldenSet[name] {
			t.Errorf("generated file %s has no golden counterpart (run GOLDEN_UPDATE=1)", name)
		}
	}
}

// TestGeneratePkOnlyTable 覆盖零 @dynamic 列的最小接入形态(表级 @pk 单独入选):
// 生成必须成功且产物可编译——不残留未使用的 "time" import 与 slots 变量。
func TestGeneratePkOnlyTable(t *testing.T) {
	req := loadGoldenRequest(t)
	users := findTable(req, "users")
	for _, c := range users.Columns {
		c.Comment = "" // 剥离全部列级指令,仅保留人类注释
	}
	users.Comment = "用户表 [@pk: id]"
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	src := mustFile(t, resp, "users_dynamic_gen.go")
	if strings.Contains(src, "\t\"time\"\n") {
		t.Errorf("pk-only table must not import \"time\" (no *time.Time params emitted)")
	}
	if strings.Contains(src, "slots :=") {
		t.Errorf("pk-only table must not reference slots (no op cases emitted)")
	}
	if strings.Contains(src, "UpdateUsers") || strings.Contains(src, "UserTable") {
		t.Errorf("pk-only table must not generate an update surface (implicit exit without @updatable)")
	}
}

// TestGenerateReadOnlyTimePK 覆盖只读表(无更新面)+ 时间类型主键:@pk 自动
// 定位矩阵只在更新面启用时生成,读面不得因此残留未使用的 "time" import
// (否则生成物无法编译)。@update: false 同样覆盖。
func TestGenerateReadOnlyTimePK(t *testing.T) {
	for _, tableComment := range []string{
		"用户表 [@pk: id]",
		"用户表 [@pk: id @update: false]",
	} {
		req := loadGoldenRequest(t)
		users := findTable(req, "users")
		for _, c := range users.Columns {
			c.Comment = ""
			if c.Name == "id" {
				c.Type = &pb.Identifier{Name: "timestamptz"}
			}
		}
		users.Comment = tableComment
		resp, err := Generate(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: Generate: %v", tableComment, err)
		}
		s := mustFile(t, resp, "users_dynamic_gen.go")
		if strings.Contains(s, "\t\"time\"\n") {
			t.Errorf("%s: read-only table must not import \"time\" (no update surface)", tableComment)
		}
		if strings.Contains(s, "UpdateUsers") {
			t.Errorf("%s: read-only table must not generate an update surface", tableComment)
		}
	}
}

// TestGeneratePkAutoLocator 覆盖 A4 的两条正向规则:
//  1. 有 @pk、只有 set-capable 列(无任何 where 声明)也能生成更新面——
//     where 资格完全由 pk 自动矩阵满足;
//  2. 显式 @updatable: auto 与裸形态等价(Set + Where)。
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

	// 显式 auto:Set + WHERE 两侧齐全(与裸形态等价)。
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

// TestGenerateScopeInAny 覆盖数组三态守卫(NULL=放行 / {}=拒绝 / 非空=ANY):
// frag 须自带外层括号(顶层 OR 不裸露于业务条件的 AND),且 fn 调用出现
// 两次 → 占位参数双份。
func TestGenerateScopeInAny(t *testing.T) {
	req := loadGoldenRequest(t)
	setTableComment(req, "users", "用户表 [@scope: id in_any fn_perm_user_ids(user_id:varchar, scopes:int[]) @pk: id]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	src := mustFile(t, resp, "users_dynamic_gen.go")
	want := `sq.Expr("(fn_perm_user_ids(?, ?) IS NULL OR id = ANY(fn_perm_user_ids(?, ?)))", params.Scope.UserID, params.Scope.Scopes, params.Scope.UserID, params.Scope.Scopes)`
	if !strings.Contains(src, want) {
		t.Errorf("in_any guard SQL mismatch.\n want fragment: %s\n absent from generated source", want)
	}
}

func TestGenerateTablesFilter(t *testing.T) {
	req := loadGoldenRequest(t)
	req.PluginOptions = []byte(`{
		"package": "gen",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
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
// 包括 datetime between、not_null、jsonb has、uuid SET cast、int_array &&
// 预设交集、同列重复取首条、显式清单去重排序、auto 不授予 null op、空串
// 模糊定位拒绝。
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
				`sq.Expr("meta @> ?::jsonb", string(v))`,
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
func TestGenerateDSLFailures(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*pb.GenerateRequest)
		wantErr string
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
			wantErr: "missing @pk",
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

// addPkTable 向 public schema 追加一张最小只读表(id bigint 主键),用于
// 构造命名/入选等组合场景。
func addPkTable(req *pb.GenerateRequest, table string) {
	for _, s := range req.Catalog.Schemas {
		if s.Name != "public" {
			continue
		}
		s.Tables = append(s.Tables, &pb.Table{
			Rel:     &pb.Identifier{Name: table},
			Comment: "表 [@pk: id]",
			Columns: []*pb.Column{{
				Name:    "id",
				NotNull: true,
				Type:    &pb.Identifier{Name: "int8"},
			}},
		})
		return
	}
	panic("public schema not found")
}
