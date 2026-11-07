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
// 本文件覆盖 golden 比对与读面(注册表 / Params / List / Count / Schema)形态;
// DSL 失败路径见 dsl_test.go,更新面见 gen_update_test.go。

const goldenOptions = `{
  "package": "gen",
  "runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
  "schema": "public",
  "default_ops": "none",
  "page": {"default": 20, "max": 200}
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
	if !strings.Contains(src, "func ListUsers(") || !strings.Contains(src, "func UsersQuerySchema(") {
		t.Error("pk-only table must still generate the read surface (List / Schema)")
	}
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
// (否则生成物无法编译)。
func TestGenerateReadOnlyTimePK(t *testing.T) {
	for _, tableComment := range []string{
		"用户表 [@pk: id]",
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
		if !strings.Contains(s, "func ListUsers(") {
			t.Errorf("%s: read-only table must still generate the read surface", tableComment)
		}
		if strings.Contains(s, "\t\"time\"\n") {
			t.Errorf("%s: read-only table must not import \"time\" (no update surface)", tableComment)
		}
		if strings.Contains(s, "UpdateUsers") {
			t.Errorf("%s: read-only table must not generate an update surface", tableComment)
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

// TestGenerateScopeJSONBParam 覆盖 @scope 的 jsonb 标识符参数:占位符显式
// cast 为 ?::jsonb,Scope 字段类型 json.RawMessage,产物因此引入
// encoding/json import(needJSON 分支)。
func TestGenerateScopeJSONBParam(t *testing.T) {
	req := loadGoldenRequest(t)
	setTableComment(req, "users", "用户表 [@scope: meta eq fn_json_eq(p:jsonb) @pk: id]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	src := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		`"encoding/json"`,
		"P json.RawMessage",
		`meta = fn_json_eq(?::jsonb)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("jsonb scope param surface missing %q", want)
		}
	}
}

// TestGenerateNumericCast 覆盖 numeric 列的参数 cast:where 值条件显式
// ?::numeric(float8 参数收敛为 numeric,列不被隐式转 float8 比较);
// float4/float8 列(score)不 cast。夹具 balance 仅声明 range,eq/in 形态
// 由末段 setComment 改写注释后覆盖。
func TestGenerateNumericCast(t *testing.T) {
	req := loadGoldenRequest(t)
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	src := mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		`sq.Expr("balance >= ?::numeric", *params.BalanceGte)`,
		`sq.Expr("balance <= ?::numeric", *params.BalanceLte)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("numeric where cast missing %q", want)
		}
	}
	if strings.Contains(src, `sq.GtOrEq{"balance"`) {
		t.Error("numeric column must not emit bare float8 comparison")
	}
	if !strings.Contains(src, `sq.GtOrEq{"score": *params.ScoreGte}`) {
		t.Error("float8 column must not gain a cast")
	}

	// eq / in 形态(setComment 改写 balance 注释):等值走 sq.Expr 单 cast,in 走 PlaceholdersCast。
	req = loadGoldenRequest(t)
	setComment(req, "users", "balance", "余额 [@dynamic: exact, in]")
	resp, err = Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate(eq/in): %v", err)
	}
	src = mustFile(t, resp, "users_dynamic_gen.go")
	for _, want := range []string{
		`sq.Expr("balance = ?::numeric", *params.Balance)`,
		`sq.Expr("balance IN ("+dynq.PlaceholdersCast(len(params.BalanceIn), "::numeric")+")", dynq.Anys(params.BalanceIn)...)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("numeric eq/in cast missing %q", want)
		}
	}
}

// TestGenerateNumericSortable 覆盖 numeric 列的 sort 能力位放行:registry 以
// DataTypeDecimal + Cast "::numeric" 发射,游标取值直通 pgtype.Numeric 模型
// 字段(编码/解码由 dynq 的 decimal 分支承担)。registry 断言以 balance 字段
// 块锚定——裸 "Sortable:   true," 对任意列都命中,归因不了 balance。
func TestGenerateNumericSortable(t *testing.T) {
	req := loadGoldenRequest(t)
	setComment(req, "users", "balance", "余额 [元] [@dynamic: range, sort @updatable: set]")
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	src := mustFile(t, resp, "users_dynamic_gen.go")
	balanceRegistry := `Key:        "balance",
			Column:     "balance",
			Label:      "balance",
			DataType:   dynq.DataTypeDecimal,
			Nullable:   true,
			Ops:        []dynq.Op{dynq.OpGt, dynq.OpGte, dynq.OpLt, dynq.OpLte, dynq.OpBetween},
			Sortable:   true,
			Visibility: dynq.VisibilityOptional,
			Hidden:     false,
			Cast:       "::numeric",`
	for _, want := range []string{
		balanceRegistry,
		"case \"balance\":\n\t\t\tvals[i] = row.Balance",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("numeric sortable surface missing %q", want)
		}
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

// TestTableNameQualified 锁定非 public schema 的表名限定形态:public 与空值
// 不带前缀,其余 schema 以点号限定。
func TestTableNameQualified(t *testing.T) {
	cases := []struct{ schema, want string }{
		{"", "users"},
		{"public", "users"},
		{"private", "private.users"},
		// 保留字 schema/表名按需引号化(见 ident.go),安全名不受影响。
		{"order", `"order".users`},
		{"private", "private.\"check\""},
	}
	for _, tc := range cases {
		opts := &Options{Schema: tc.schema}
		name := "users"
		if strings.Contains(tc.want, `"check"`) {
			name = "check"
		}
		if got := tableNameQualified(name, opts); got != tc.want {
			t.Errorf("schema %q: got %q, want %q", tc.schema, got, tc.want)
		}
	}
}

// 保留字表名/列名_按需引号化_产物SQL合法:表 order(RESERVED)与列
// check(RESERVED)的物理名发射进 SQL 文本时统一加双引号;裸名与逻辑键
// (camelCase 键、错误文案、指令回显)不受影响。
func TestGenerateReservedWordNamesQuotedInSQL(t *testing.T) {
	req := loadGoldenRequest(t)
	addPkTable(req, "order")
	ord := findTable(req, "order")
	ord.Columns = append(ord.Columns, &pb.Column{
		Name:    "check",
		NotNull: true,
		Type:    &pb.Identifier{Name: "int8"},
	})
	resp, err := Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	src := mustFile(t, resp, "order_dynamic_gen.go")
	for _, want := range []string{
		`Table:    "\"order\""`,
		`sq.Select(cols...).From("\"order\"")`,
		`sq.Select("COUNT(*)").From("\"order\"")`,
		`Column:     "\"check\""`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("quoted identifier surface missing %q", want)
		}
	}
	// 逻辑键与裸物理名的既有契约不变:registry 键仍为 camelCase 逻辑键。
	for _, want := range []string{`Key:        "check"`, `Resource: "order"`} {
		if !strings.Contains(src, want) {
			t.Errorf("logical key contract changed, missing %q", want)
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
