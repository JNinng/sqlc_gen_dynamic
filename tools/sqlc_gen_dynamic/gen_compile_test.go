package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	pb "github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// 生成物编译闸(U6/U9):golden 只做字节比较,不验证产物可编译;本文件把
// 生成源码写入临时包并执行 go build,用最小模型桩(DBTX / 模型结构体 /
// 枚举类型)覆盖各分支:只读时间主键、datetime between、not_null、jsonb has、
// 跨包 model_import。桩由 TableSpec 派生,与生成面自动同齐。
//
// 编译在独立临时模块中进行(go.mod 以 replace 指回仓库根,dynq 恒取本地实现),
// 依赖版本解析自主模块 go.mod:运行期才出现的生成包不能静态进入主模块的
// 依赖图,tidy/build 均只写临时目录,不触碰仓库 go.mod。
// short 模式与 go 缺失时跳过。

func TestGeneratedCodeCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skip go build")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}

	// 只读时间主键:零更新面,timestamptz 主键,排序 + 游标取值走 time.Time。
	t.Run("read_only_time_pk", func(t *testing.T) {
		buildGeneratedPackage(t, func(req *pb.GenerateRequest) {
			scopeToTable(req, "users")
			users := findTable(req, "users")
			for _, c := range users.Columns {
				c.Comment = ""
				if c.Name == "id" {
					c.Type = &pb.Identifier{Name: "timestamptz"}
				}
			}
			setComment(req, "users", "created_at", "创建时间 [@dynamic: range @sortable]")
			users.Comment = "用户表 [@default_sort: created_at DESC @pk: id]"
		}, false)
	})

	// 更新面 op 分支:datetime between / not_null、jsonb has(含空值守卫)、
	// json(非 jsonb)列 has 的左列 cast、全值域读面 Params。
	t.Run("update_op_family", func(t *testing.T) {
		buildGeneratedPackage(t, func(req *pb.GenerateRequest) {
			scopeToTable(req, "users")
			users := findTable(req, "users")
			for _, c := range users.Columns {
				c.Comment = ""
			}
			setComment(req, "users", "name", "姓名 [@updatable]")
			setComment(req, "users", "updated_at", "更新时间 [@dynamic: range, is_null, not_null @sortable @updatable: between, not_null]")
			setComment(req, "users", "meta", "元数据 [@dynamic: has @updatable: has]")
			addColumn(req, "users", "extra", "json", true)
			setComment(req, "users", "extra", "扩展 [@dynamic: has @updatable: has]")
			users.Comment = "用户表 [@default_sort: updated_at DESC @pk: id]"
		}, false)
	})

	// 跨包 model_import(U9):全部入选表 + models 包限定引用,一并编译。
	// override 取 set-only 列(Set 签名引用 uuid.UUID,Set 值经 any 透传):
	// 端到端验证"SetCapable → 收集 import 且被签名引用"。带读面值 op 的
	// override 由生成期分叉检测拒绝(见 TestGoTypeOverrideDivergence)。
	t.Run("model_import", func(t *testing.T) {
		buildGeneratedPackage(t, func(req *pb.GenerateRequest) {
			setComment(req, "users", "name", "姓名 [@updatable: set]")
			req.PluginOptions = []byte(`{
				"package": "gen",
				"model_import": "compile/models",
				"model_package": "models",
				"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
				"go_type_overrides": [
					{"column": "users.name", "go_type": {"import": "github.com/google/uuid", "package": "uuid", "type_name": "UUID"}}
				]
			}`)
		}, true)
	})
}

// scopeToTable 把生成范围收窄到单表(桩与编译面最小化)。
func scopeToTable(req *pb.GenerateRequest, table string) {
	req.PluginOptions = []byte(fmt.Sprintf(`{
		"package": "gen",
		"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq",
		"tables": [%q]
	}`, table))
}

// buildGeneratedPackage 生成 → 落盘(生成物 + 模型桩)→ go build。
// modelImport 时把 model_import 指向临时模块内的 models 桩包,
// 产物以 models. 前缀引用模型与 DBTX。
func buildGeneratedPackage(t *testing.T, mutate func(*pb.GenerateRequest), modelImport bool) {
	t.Helper()
	// 临时模块必须建在 t.TempDir() 下而非包目录内:工作树中存在"有 .go 文件
	// 但尚无 go.mod"的目录时,并发的 go test ./... / go vet / gopls 会把它
	// 枚举进父模块而报错,进程被硬杀时还会留下污染 git status 的残留。
	base := filepath.Join(t.TempDir(), "mod")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("create temp module dir: %v", err)
	}

	req := loadGoldenRequest(t)
	if modelImport {
		req.PluginOptions = []byte(`{
			"package": "gen",
			"model_import": "compile/models",
			"model_package": "models",
			"runtime_import": "github.com/jninng/sqlc_gen_dynamic/dynq"
		}`)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := Generate(context.Background(), req)
	writeRawOnFail(t, err)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, f := range resp.Files {
		path := filepath.Join(base, f.Name)
		if err := os.WriteFile(path, f.Contents, 0o644); err != nil {
			t.Fatalf("write %s: %v", f.Name, err)
		}
	}

	opts, err := ParseOptions(req)
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	specs, _, err := buildTableSpecs(req, opts)
	if err != nil {
		t.Fatalf("buildTableSpecs: %v", err)
	}
	if modelImport {
		modelsDir := filepath.Join(base, "models")
		if err := os.MkdirAll(modelsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeCompileStubs(t, specs, opts, modelsDir, "models")
	} else {
		writeCompileStubs(t, specs, opts, base, opts.Package)
	}

	writeCompileGoMod(t, base)
	runCompileCmd(t, base, "go", "mod", "tidy")
	runCompileCmd(t, base, "go", "build", ".")
}

// writeCompileGoMod 写出临时模块的 go.mod:依赖版本解析自主模块 go.mod,
// dynq 以 replace 指回仓库根(与 example 工程同一自举纪律)。
func writeCompileGoMod(t *testing.T, dir string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	mainGoMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read main go.mod: %v", err)
	}
	version := func(path string) string {
		re := regexp.MustCompile(regexp.QuoteMeta(path) + ` (v[0-9][^ \n]*)`)
		if m := re.FindSubmatch(mainGoMod); m != nil {
			return string(m[1])
		}
		t.Fatalf("go.mod: dependency %q not found", path)
		return ""
	}
	goLine := regexp.MustCompile(`(?m)^go ([0-9.]+)`)
	if m := goLine.FindSubmatch(mainGoMod); m == nil {
		t.Fatal("go.mod: go directive not found")
	}
	goVersion := string(goLine.FindSubmatch(mainGoMod)[1])
	rootAbs := filepath.ToSlash(root)
	content := fmt.Sprintf(`module compile

go %s

require (
	github.com/Masterminds/squirrel %s
	github.com/jackc/pgx/v5 %s

	github.com/jninng/sqlc_gen_dynamic v0.0.0
)

replace github.com/jninng/sqlc_gen_dynamic => %s
`, goVersion, version("github.com/Masterminds/squirrel"), version("github.com/jackc/pgx/v5"), rootAbs)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

func runCompileCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// writeCompileStubs 写出编译所需的最小模型桩:DBTX 接口、模型结构体(字段集
// 由 TableSpec 派生,类型从 GoType 剥离模型包前缀)、枚举类型。仅保证可编译,
// 不承诺运行期扫描语义。
func writeCompileStubs(t *testing.T, specs []*TableSpec, opts *Options, dir, pkg string) {
	t.Helper()
	needTime := false
	for _, spec := range specs {
		for _, c := range spec.Columns {
			if strings.Contains(c.GoType, "time.Time") {
				needTime = true
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"context\"\n\n")
	if needTime {
		b.WriteString("\t\"time\"\n\n")
	}
	b.WriteString("\t\"github.com/jackc/pgx/v5\"\n\t\"github.com/jackc/pgx/v5/pgconn\"\n)\n\n")
	b.WriteString("// DBTX 是生成面引用的连接接口桩(与 sqlc-gen-go 的 db.go 同形态)。\n")
	b.WriteString("type DBTX interface {\n")
	b.WriteString("\tExec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)\n")
	b.WriteString("\tQuery(ctx context.Context, sql string, args ...any) (pgx.Rows, error)\n")
	b.WriteString("\tQueryRow(ctx context.Context, sql string, args ...any) pgx.Row\n")
	b.WriteString("}\n\n")

	strip := func(s string) string { return strings.TrimPrefix(s, opts.ModelPackage+".") }
	enumSeen := map[string]bool{}
	for _, spec := range specs {
		fmt.Fprintf(&b, "type %s struct {\n", spec.ModelName)
		for _, c := range spec.Columns {
			// override 列的桩字段用 any:其外部类型属生成包的 import 面,
			// 桩包不为此管理 import(生成面已由产物自身引用并编译)。
			typ := "any"
			switch {
			case c.OverrideImport != nil:
			case c.DataType == "enum":
				typ = strip(c.EnumGoType)
			case c.DataType != "unmapped":
				typ = strip(c.GoType)
			}
			fmt.Fprintf(&b, "\t%s %s\n", c.ModelField, typ)
		}
		b.WriteString("}\n\n")
	}
	for _, spec := range specs {
		for _, c := range spec.Columns {
			if c.EnumGoType == "" {
				continue
			}
			name := strip(c.EnumGoType)
			if enumSeen[name] {
				continue
			}
			enumSeen[name] = true
			fmt.Fprintf(&b, "type %s string\n\n", name)
		}
	}
	path := filepath.Join(dir, "compile_stubs.go")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write stubs: %v", err)
	}
}
