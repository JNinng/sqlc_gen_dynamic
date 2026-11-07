package main

import (
	"fmt"
	"strings"
)

// codegen.go:生成源码的写入原语。
//
// 分工固定为:对任何表都一样的静态文本以 Raw 写入(templates.go 中的骨架
// 常量),逐列 / 逐 op 的差异用片段函数与 Linef 表达。产物最终经 go/format
// 归一,因此生成器自身的缩进不参与生成契约,只影响失败时的原始源码可读性。

// printer 累积一段生成源码。
type printer struct {
	b strings.Builder
}

// Line 写入一行。
func (p *printer) Line(s string) {
	p.b.WriteString(s)
	p.b.WriteByte('\n')
}

// Linef 写入一行格式化文本。格式串为常量字面量时由 go test 内置的 printf
// vet 校验实参,避免静默产出错误的源码文本。
func (p *printer) Linef(format string, args ...any) {
	fmt.Fprintf(&p.b, format, args...)
	p.b.WriteByte('\n')
}

// Raw 原样写入一个静态块,只为缺失结尾换行的块补一个换行。
func (p *printer) Raw(block string) {
	if block == "" {
		return
	}
	p.b.WriteString(block)
	if !strings.HasSuffix(block, "\n") {
		p.b.WriteByte('\n')
	}
}

// Blank 写入一个空行。
func (p *printer) Blank() {
	p.b.WriteByte('\n')
}

// Indented 把片段逐行加上 prefix 后写入,用于把片段嵌入指定嵌套层级。
// 空片段不产生任何输出。
func (p *printer) Indented(prefix, block string) {
	if block == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		p.b.WriteString(prefix)
		p.b.WriteString(line)
		p.b.WriteByte('\n')
	}
}

// String 返回累积的源码。
func (p *printer) String() string { return p.b.String() }

// fill 以 {{name}} 占位符替换骨架文本。占位符名在调用点显式给出,避免长骨架
// 的顺序实参错位;替换后仍残留占位符说明名字写错,直接 panic——错误必须在
// 生成期暴露,而不是静默落进产物(注释里的残留不会让 gofmt 报错)。
func fill(skel string, kv ...string) string {
	if len(kv)%2 != 0 {
		panic("fill: kv must be name/value pairs")
	}
	for i := 0; i < len(kv); i += 2 {
		skel = strings.ReplaceAll(skel, "{{"+kv[i]+"}}", kv[i+1])
	}
	if strings.Contains(skel, "{{") {
		panic("fill: unresolved placeholder in skeleton:\n" + skel)
	}
	return skel
}
