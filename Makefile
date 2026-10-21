# sqlc_gen_dynamic 工程化入口(§13)
#
#   make sqlc          构建插件 + sqlc generate 全量生成
#   make test          全量测试(dynq 单测 / 插件 golden;无数据库依赖,example 包随测编译)
#   make check         生成后 git diff 锁 gen 目录,防 schema 漂移
#   make release-wasm  编译 wasm 产物 + sha256(发布期 sqlc.yaml 以 url+sha256 锁定)
#
# 约定:命令在仓库根目录执行;sqlc.yaml 中 process.cmd 相对运行目录解析。

SQLC       ?= sqlc
GO         ?= go
VERSION    ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
PLUGIN_EXE  = bin/sqlc_gen_dynamic$(if $(filter Windows_NT,$(OS)),.exe,)
WASM        = bin/sqlc_gen_dynamic.wasm
EXE_SHA     = bin/sqlc_gen_dynamic.exe.sha256

.PHONY: sqlc test check release-wasm clean

sqlc: $(PLUGIN_EXE)
	$(SQLC) generate -f example/db/sqlc.yaml

$(PLUGIN_EXE):
	$(GO) build -o $@ ./tools/sqlc_gen_dynamic

test:
	$(GO) test ./...
	$(GO) -C example test ./...

# 生成物提交入库;check 保证工作区生成物与 DDL 无漂移(CI 用固定版本 sqlc 执行)。
check: sqlc
	git diff --exit-code -- example/db/gen

$(WASM):
	GOOS=wasip1 GOARCH=wasm $(GO) build -o $@ ./tools/sqlc_gen_dynamic

# 发布资产以版本命名(GitHub Release 上传件,与 usage.md 锁定示例同名);
# VERSION 缺省取最近 tag,打完 v0.2.0 tag 后执行即为正确版本名。
release-wasm: $(WASM)
	sha256sum $(WASM) > $(WASM).sha256
	cat $(WASM).sha256
	cp $(WASM) bin/sqlc_gen_dynamic-$(VERSION).wasm
	cp $(WASM).sha256 bin/sqlc_gen_dynamic-$(VERSION).wasm.sha256

clean:
	rm -f $(PLUGIN_EXE) $(WASM) $(WASM).sha256 $(EXE_SHA) bin/sqlc_gen_dynamic-*.wasm bin/sqlc_gen_dynamic-*.wasm.sha256
