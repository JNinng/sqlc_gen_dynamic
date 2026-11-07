package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// 注释 DSL(§5.2/§5.3):指令必须包裹在 [ ] 中且内容以 @ 起始;
// 不以 @ 起始的方括号是纯文档文本,不参与生成。
// 注释只承载能力指令——label / desc / order / visibility / options / api
// 等 UI 元数据来自 sqlc.yaml 的 codegen.options.ui(见 options.go)。
//
// 列级:<人类文本> [@dynamic: <sort,项,项,...>] [@internal]
//       [@updatable[: set|locate|<项,项,...>]]
// 表级:[@default_sort: <字段> <ASC|DESC>] [@pk: <列>]
//       [@scope: <列> <op> <函数>(<参数:类型|字面量>,...) ...]
//       [@filter: <列> <op> [字面量] ...]

type dslTag struct {
	Name    string
	Payload string
}

// parseComment 解析注释,返回指令标签序列。
// 方括号配平是注释层硬约束:任何未配平的 [ / ](单引号字符串内部除外)
// → 生成期 fail,定位到表/列;[ ] 内出现白名单外的 @xxx → fail。
func parseComment(where, comment string) ([]dslTag, error) {
	if err := checkBracketBalance(where, comment); err != nil {
		return nil, err
	}
	var tags []dslTag
	rest := comment
	for {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			break
		}
		rel := findBlockClose(rest[open+1:])
		if rel < 0 {
			// 配平检查已保证可达性;保留兜底,防御未来修改破坏两者一致性。
			return nil, fmt.Errorf("%s: unclosed '[' in comment %q", where, comment)
		}
		close := open + 1 + rel
		block := strings.TrimSpace(rest[open+1 : close])
		if strings.HasPrefix(block, "@") {
			blockTags, err := parseDirectiveBlock(block)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", where, err)
			}
			tags = append(tags, blockTags...)
		}
		rest = rest[close+1:]
	}
	return tags, nil
}

// checkBracketBalance 校验整条注释的方括号配平。单引号字符串内部不参与
// 计数(@scope / @filter 的字面量可含 [ / ]);引号只在指令块内(depth>0)
// 才视为字符串定界符——块外的人类文本撇号(如 it's / don't)不能吞掉方括号,
// 否则裸 ] 会被漏检或合法字面量被误报。
func checkBracketBalance(where, comment string) error {
	depth := 0
	for i := 0; i < len(comment); i++ {
		switch comment[i] {
		case '\'':
			if depth > 0 {
				if j := strings.IndexByte(comment[i+1:], '\''); j >= 0 {
					i += j + 1 // 跳到闭引号,内部方括号不计
				}
			}
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return fmt.Errorf("%s: unmatched ']' in comment %q", where, comment)
			}
			depth--
		}
	}
	if depth != 0 {
		return fmt.Errorf("%s: unclosed '[' in comment %q", where, comment)
	}
	return nil
}

// findBlockClose 返回块终止 ']' 在 s(块内容,不含开括号)中的下标;
// 块内成对的 [ ] 不终止块(如 @scope 类型标记 int[]/varchar[]),
// 单引号字符串内部的方括号不计,仅未配对的 ']' 终止。
func findBlockClose(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if j := strings.IndexByte(s[i+1:], '\''); j >= 0 {
				i += j + 1
			}
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// parseDirectiveBlock 解析以 @ 起始的指令块:@name 或 @name: payload。
// payload 可含空格(如 "created_at DESC"),直到下一个 @token 结束。
func parseDirectiveBlock(block string) ([]dslTag, error) {
	words := strings.Fields(block)
	var tags []dslTag
	cur := -1
	for _, w := range words {
		if strings.HasPrefix(w, "@") {
			tags = append(tags, dslTag{})
			cur = len(tags) - 1
			name := strings.TrimPrefix(w, "@")
			if i := strings.IndexByte(name, ':'); i >= 0 {
				tags[cur].Name = name[:i]
				tags[cur].Payload = name[i+1:]
			} else {
				tags[cur].Name = name
			}
			if tags[cur].Name == "" {
				return nil, fmt.Errorf("empty directive name in %q", block)
			}
			continue
		}
		if cur < 0 {
			return nil, fmt.Errorf("directive block %q must start with @", block)
		}
		if tags[cur].Payload == "" {
			tags[cur].Payload = w
		} else {
			tags[cur].Payload += " " + w
		}
	}
	return tags, nil
}

// validateTagNames 校验指令名在作用域白名单内,杜绝静默忽略导致的能力丢失。
// 注释只承载能力指令:UI 元数据走 options.ui,旧名与已删指令一律按未知
// 指令 fail(无兼容期)。
func validateTagNames(where, scope string, tags []dslTag) error {
	var allowed map[string]bool
	if scope == "column" {
		allowed = map[string]bool{
			"dynamic": true, "internal": true, "updatable": true,
		}
	} else {
		allowed = map[string]bool{
			"default_sort": true, "pk": true, "scope": true, "filter": true,
		}
	}
	for _, t := range tags {
		if !allowed[t.Name] {
			var names []string
			for name := range allowed {
				names = append(names, "@"+name)
			}
			sort.Strings(names) // 排序保证报错信息稳定
			return fmt.Errorf("%s: unknown directive @%s (allowed for %s: %s)",
				where, t.Name, scope, strings.Join(names, ", "))
		}
	}
	return nil
}

// mergeTags 同名指令去重(声明一次等价),不同指令块按出现顺序合并。
func mergeTags(tags []dslTag) []dslTag {
	var out []dslTag
	seen := map[string]bool{}
	for _, t := range tags {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	return out
}

func findTag(tags []dslTag, name string) (dslTag, bool) {
	for _, t := range tags {
		if t.Name == name {
			return t, true
		}
	}
	return dslTag{}, false
}

// updatableModes 是 @updatable 的保留形态词:保留字只能单独出现,
// 与显式 op 清单混写 → 生成期 fail。
var updatableModes = map[string]bool{"set": true, "locate": true}

// parseUpdatableMode 解析 @updatable 的四形态:
//   - 裸              → SET + WHERE(类型能力矩阵);
//   - set             → 仅 SET;
//   - locate          → 仅 WHERE(类型能力矩阵);
//   - <preset|op>, ... → SET + WHERE(显式清单)。
//
// 已删除的拼写(auto / false / where)给出定向错误,不做兼容解析。
func parseUpdatableMode(where string, payload string) (string, error) {
	switch payload {
	case "":
		return "auto", nil
	case "set", "locate":
		return payload, nil
	case "auto":
		return "", fmt.Errorf("%s: @updatable: auto was removed; bare @updatable is already auto (drop the keyword)", where)
	case "false":
		return "", fmt.Errorf("%s: @updatable: false was removed; omit the directive instead (no @updatable means no update capability)", where)
	case "where":
		return "", fmt.Errorf("%s: @updatable: where was renamed to locate; write @updatable: locate", where)
	}
	for _, raw := range strings.Split(payload, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			return "", fmt.Errorf("%s: empty @updatable item", where)
		}
		switch item {
		case "auto":
			return "", fmt.Errorf("%s: @updatable: auto was removed; bare @updatable is already auto (drop the keyword)", where)
		case "false":
			return "", fmt.Errorf("%s: @updatable: false was removed; omit the directive instead (no @updatable means no update capability)", where)
		case "where":
			return "", fmt.Errorf("%s: @updatable: where was renamed to locate; write @updatable: locate", where)
		}
		if updatableModes[item] {
			return "", fmt.Errorf("%s: @updatable reserved mode %q must appear alone (got %q)", where, item, payload)
		}
	}
	return "list", nil
}

// updatableEcho 把解析后的 @updatable 形态重建为规范文本(策略来源回显):
// auto 回显裸形态,保留形态回显声明词,显式清单回显展开后的 op 集。
func updatableEcho(c *ColumnSpec) string {
	switch c.UpdatableMode {
	case "auto":
		return "@updatable"
	case "set", "locate":
		return "@updatable: " + c.UpdatableMode
	default:
		return "@updatable: " + strings.Join(c.UpdateOps, ", ")
	}
}

// APISpec 是 options.ui 的 api 声明(插件侧;dynq.FieldAPI 为运行期形态)。
// 声明校验见 catalog.go 的 validateUIAPI。
type APISpec struct {
	Endpoint string
	ValueKey string
	LabelKey string
	Mode     string // single | multi | search;空 = 由 ops 推导
}

// parseDefaultSortDirective 解析 @default_sort: "<column> <ASC|DESC>"。
// 目标列为物理列名;存在性、非 @internal 与可排序类型在表级校验。
// 目标列隐式获得排序能力,无需列级声明。
func parseDefaultSortDirective(where string, t dslTag) (column string, desc bool, err error) {
	parts := strings.Fields(t.Payload)
	if len(parts) != 2 {
		return "", false, fmt.Errorf("%s: @default_sort wants \"<column> <ASC|DESC>\", got %q", where, t.Payload)
	}
	switch strings.ToLower(parts[1]) {
	case "asc":
		desc = false
	case "desc":
		desc = true
	default:
		return "", false, fmt.Errorf("%s: @default_sort direction must be ASC or DESC, got %q", where, parts[1])
	}
	return parts[0], desc, nil
}

// OptionSpec 是一项静态字典项(生成期形态;dynq.FieldOption 为运行期形态)。
// 来源为 options.ui 的 options 列表,value 按列 dataType 定型:
// integer → int64,string/uuid → string。
type OptionSpec struct {
	Value any
	Label string
}

// scopeGuard 是 @scope 声明的一项守卫:目标列、谓词操作符、守卫函数与实参列表。
type scopeGuard struct {
	Column string
	Op     string // in | in_any | eq | gt | gte | lt | lte
	Fn     string
	Args   []scopeArg
}

// scopeArg 是守卫函数实参:标识符参数(Name+Type,值来自生成的 Scope 结构体
// 对应字段)或字面量参数(Quoted 区分字符串/整数,校验后内联进 SQL)。
type scopeArg struct {
	Name    string
	Type    string // 类型词表标记;字面量参数为空
	Literal string // 字面量原文;标识符参数为空
	Quoted  bool   // 字面量为单引号字符串(true)或整数(false)
}

// scopeOps 是守卫谓词操作符的封闭集合:SQL 语义与业务操作符表同源。
// in_any 是数组三态守卫(fn 返回数组):NULL=放行全部、{}=拒绝全部、
// 非空=ANY 匹配;与 in(集合返回函数,NULL 行=fail-closed 拒绝)方向相反,
// 供"NULL 表示全租户"一类的显式放行契约使用。
// 刻意排除 neq/not_in——NOT IN 撞上集合含 NULL 时恒空,是权限事故配方。
var scopeOps = map[string]bool{"in": true, "in_any": true, "eq": true, "gt": true, "gte": true, "lt": true, "lte": true}

// scopeParamTypes 是标识符参数的类型词表(DSL 标记 → Go 类型),封闭集合:
// 词表外类型 → 生成期 fail。int 对应 int64(PG 整数全量承载)。
var scopeParamTypes = map[string]string{
	"varchar":   "string",
	"int":       "int64",
	"bool":      "bool",
	"varchar[]": "[]string",
	"int[]":     "[]int64",
	"jsonb":     "json.RawMessage",
}

// fnNameRe 是守卫函数名的白名单:小写标识符,可选一层 schema 限定。
// 函数名会被拼进 SQL 字面量,必须严格校验,杜绝引号/分号/括号等注入面。
var fnNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// scopeNameRe 是列名/参数名的白名单(snake_case)。
var scopeNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// scopeIntRe 匹配整数字面量(可负),用于分词归类。
var scopeIntRe = regexp.MustCompile(`^-?[0-9]+$`)

// scopeTokenKind 标记 @scope 微语法的 token 类型。
type scopeTokenKind int

const (
	tokIdent scopeTokenKind = iota
	tokString
	tokInt
	tokLParen
	tokRParen
	tokComma
	tokColon
)

type scopeToken struct {
	kind scopeTokenKind
	text string
}

// scanScopeTokens 是 @scope payload 的分词器:标识符(可含 . [] 供 schema
// 限定与数组类型)、单引号字符串(内部禁引号,不支持转义)、整数(可负)、
// 圆括号/逗号/冒号。注意:上游指令块按空白粗分词,字符串字面量内的连续
// 空格会被折叠,字面量应避免连续空格。
func scanScopeTokens(where, payload string) ([]scopeToken, error) {
	var toks []scopeToken
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...))
	}
	rs := strings.NewReader(payload)
	for {
		c, _, err := rs.ReadRune()
		if err != nil {
			break
		}
		switch {
		case unicode.IsSpace(c):
			continue
		case c == '(':
			toks = append(toks, scopeToken{tokLParen, "("})
		case c == ')':
			toks = append(toks, scopeToken{tokRParen, ")"})
		case c == ',':
			toks = append(toks, scopeToken{tokComma, ","})
		case c == ':':
			toks = append(toks, scopeToken{tokColon, ":"})
		case c == '\'':
			var b strings.Builder
			closed := false
			for {
				n, _, err := rs.ReadRune()
				if err != nil {
					break
				}
				if n == '\'' {
					closed = true
					break
				}
				b.WriteRune(n)
			}
			if !closed {
				return nil, fail("unterminated string literal in @scope payload %q", payload)
			}
			// 内嵌引号在扫描层即不可能:字符串于首个闭引号终止,残片会以
			// "缺少 , 或 )" 的语法错误暴露,无注入面。
			toks = append(toks, scopeToken{tokString, b.String()})
		default:
			if !(isScopeIdentRune(c, true) || unicode.IsDigit(c) || c == '-') {
				return nil, fail("unexpected character %q in @scope payload %q", string(c), payload)
			}
			var b strings.Builder
			b.WriteRune(c)
			for {
				n, _, err := rs.ReadRune()
				if err != nil {
					break
				}
				if !isScopeIdentRune(n, false) && !unicode.IsDigit(n) {
					rs.UnreadRune()
					break
				}
				b.WriteRune(n)
			}
			if scopeIntRe.MatchString(b.String()) {
				toks = append(toks, scopeToken{tokInt, b.String()})
			} else {
				toks = append(toks, scopeToken{tokIdent, b.String()})
			}
		}
	}
	return toks, nil
}

// isScopeIdentRune 判定标识符可续字符:字母/下划线,外加 .(schema 限定函数名)
// 与 [ ](数组类型标记 varchar[]/int[])。首字符仅允许字母/下划线/负号(由调用方处理)。
func isScopeIdentRune(c rune, first bool) bool {
	if unicode.IsLetter(c) || c == '_' {
		return true
	}
	if !first && (c == '.' || c == '[' || c == ']') {
		return true
	}
	return false
}

// parseScopeDirective 解析 @scope: "<列> <op> <函数>(<参数,...>) ..."。
//   - 列:目标物理列名(snake_case),存在性在表级校验;同列仅允许一条守卫;
//   - op:封闭集合 in|in_any|eq|gt|gte|lt|lte(in = 集合守卫,in_any = 数组三态
//     守卫,其余为标量比较);
//   - 函数:小写标识符(可带一层 schema 限定);本体不在 sqlc catalog,
//     不做存在性校验(与 options.ui api endpoint 同一信任级别),由部署侧保证;
//   - 实参:标识符参数 "name:type"(类型取词表,值来自生成的 Scope 结构体)
//     或字面量参数('字符串'/整数,校验后内联进 SQL)。允许零参。
func parseScopeDirective(where string, t dslTag) ([]scopeGuard, error) {
	toks, err := scanScopeTokens(where, t.Payload)
	if err != nil {
		return nil, err
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...))
	}
	pos := 0
	next := func() (scopeToken, bool) {
		if pos >= len(toks) {
			return scopeToken{}, false
		}
		tok := toks[pos]
		pos++
		return tok, true
	}
	expectKind := func(kind scopeTokenKind, what string) (scopeToken, error) {
		tok, ok := next()
		if !ok {
			return scopeToken{}, fail("@scope wants %s, got end of payload", what)
		}
		if tok.kind != kind {
			return scopeToken{}, fail("@scope wants %s, got %q", what, tok.text)
		}
		return tok, nil
	}
	var guards []scopeGuard
	seenColumn := map[string]bool{}
	for pos < len(toks) {
		colTok, err := expectKind(tokIdent, "a column name")
		if err != nil {
			return nil, err
		}
		if !scopeNameRe.MatchString(colTok.text) {
			return nil, fail("@scope column %q is not a snake_case identifier", colTok.text)
		}
		if seenColumn[colTok.text] {
			return nil, fail("@scope column %q guarded twice (one guard per column)", colTok.text)
		}
		opTok, err := expectKind(tokIdent, "an operator (in|eq|gt|gte|lt|lte)")
		if err != nil {
			return nil, err
		}
		if !scopeOps[opTok.text] {
			return nil, fail("@scope op %q not allowed (want one of: eq, gt, gte, in, in_any, lt, lte)", opTok.text)
		}
		fnTok, err := expectKind(tokIdent, "a guard function name")
		if err != nil {
			return nil, err
		}
		if !fnNameRe.MatchString(fnTok.text) {
			return nil, fail("@scope function name %q is not a plain (optionally schema-qualified) lowercase identifier", fnTok.text)
		}
		if _, err := expectKind(tokLParen, "'('"); err != nil {
			return nil, err
		}
		g := scopeGuard{Column: colTok.text, Op: opTok.text, Fn: fnTok.text}
		// 实参列表:零参 fn() 或 arg{, arg};实参为标识符参数 name:type
		// 或字面量('字符串'/整数)。
		first, ok := next()
		if !ok {
			return nil, fail("@scope argument list is missing ')'")
		}
		if first.kind != tokRParen {
			pos-- // 回退首个实参 token,进入实参循环
			for {
				tok, ok := next()
				if !ok {
					return nil, fail("@scope argument list is missing ')'")
				}
				switch tok.kind {
				case tokString:
					g.Args = append(g.Args, scopeArg{Literal: tok.text, Quoted: true})
				case tokInt:
					if _, perr := strconv.ParseInt(tok.text, 10, 64); perr != nil {
						return nil, fail("@scope integer literal %q out of range", tok.text)
					}
					g.Args = append(g.Args, scopeArg{Literal: tok.text})
				case tokIdent:
					if !scopeNameRe.MatchString(tok.text) {
						return nil, fail("@scope parameter name %q is not a snake_case identifier", tok.text)
					}
					if _, err := expectKind(tokColon, "':' after parameter name"); err != nil {
						return nil, err
					}
					typeTok, err := expectKind(tokIdent, "a parameter type")
					if err != nil {
						return nil, err
					}
					if _, known := scopeParamTypes[typeTok.text]; !known {
						return nil, fail("@scope parameter type %q not in vocabulary (want one of: bool, int, int[], jsonb, varchar, varchar[])", typeTok.text)
					}
					g.Args = append(g.Args, scopeArg{Name: tok.text, Type: typeTok.text})
				default:
					return nil, fail("@scope argument must be name:type or a 'string'/integer literal, got %q", tok.text)
				}
				sep, ok := next()
				if !ok {
					return nil, fail("@scope argument list is missing ')'")
				}
				if sep.kind == tokRParen {
					break
				}
				if sep.kind != tokComma {
					return nil, fail("@scope wants ',' or ')' between arguments, got %q", sep.text)
				}
			}
		}
		seenColumn[g.Column] = true
		guards = append(guards, g)
	}
	if len(guards) == 0 {
		return nil, fail(`@scope wants "<column> <op> <fn>(<args>) ...", got %q`, t.Payload)
	}
	return guards, nil
}

// scopeEcho 把解析后的守卫重建为规范文本,用于生成文件头部的策略来源回显。
func scopeEcho(guards []scopeGuard) string {
	parts := make([]string, len(guards))
	for i, g := range guards {
		args := make([]string, len(g.Args))
		for j, a := range g.Args {
			if a.Literal != "" {
				if a.Quoted {
					args[j] = "'" + a.Literal + "'"
				} else {
					args[j] = a.Literal
				}
			} else {
				args[j] = a.Name + ":" + a.Type
			}
		}
		parts[i] = fmt.Sprintf("%s %s %s(%s)", g.Column, g.Op, g.Fn, strings.Join(args, ", "))
	}
	return strings.Join(parts, " ")
}

// filterDirective 是 @filter 声明的一项服务端固定条件:目标列、谓词操作符与
// 内联字面量(is_null/not_null 不带值)。
type filterDirective struct {
	Column  string
	Op      string // eq | gt | gte | lt | lte | is_null | not_null
	Literal string // eq/比较类的字面量原文;is_null/not_null 为空
	Quoted  bool   // 字面量为单引号字符串(true)或整数(false)
}

// filterOps 是 @filter 谓词操作符的封闭集合。刻意排除 neq/not_in(NOT IN 撞
// NULL 恒空,与 @scope 同一理由)与 in(无字面量列表形态)。
var filterOps = map[string]bool{"eq": true, "gt": true, "gte": true, "lt": true, "lte": true, "is_null": true, "not_null": true}

// parseFilterDirective 解析 @filter: "<列> <op> [字面量] ..." 多子句并列,
// 复用 @scope 分词器(scanScopeTokens;引号字面量内连续空格折叠为同一既有限制)。
//   - 列:目标物理列名(snake_case),存在性在表级校验;同列至多一条子句
//     (一条不变量一列),重复 → 生成期 fail;
//   - op:封闭集合 eq|gt|gte|lt|lte|is_null|not_null;
//   - is_null/not_null 不带值;eq/比较类恰带一个字面量('字符串'/整数),
//     经分词器校验后内联。
func parseFilterDirective(where string, t dslTag) ([]filterDirective, error) {
	toks, err := scanScopeTokens(where, t.Payload)
	if err != nil {
		return nil, err
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...))
	}
	var out []filterDirective
	seenColumn := map[string]bool{}
	pos := 0
	for pos < len(toks) {
		colTok := toks[pos]
		pos++
		if colTok.kind != tokIdent || !scopeNameRe.MatchString(colTok.text) {
			return nil, fail("@filter column %q is not a snake_case identifier", colTok.text)
		}
		if seenColumn[colTok.text] {
			return nil, fail("@filter column %q filtered twice (one invariant per column)", colTok.text)
		}
		if pos >= len(toks) {
			return nil, fail("@filter wants an operator after column %q (one of: eq, gt, gte, is_null, lt, lte, not_null)", colTok.text)
		}
		opTok := toks[pos]
		pos++
		if opTok.kind != tokIdent || !filterOps[opTok.text] {
			return nil, fail("@filter op %q not allowed (want one of: eq, gt, gte, is_null, lt, lte, not_null)", opTok.text)
		}
		f := filterDirective{Column: colTok.text, Op: opTok.text}
		if opTok.text == "is_null" || opTok.text == "not_null" {
			// 不带值:下一 token 若存在,必为字面量形态(新子句以列名起始,
			// 列名为标识符)——字面量出现在此即违规。
			if pos < len(toks) && toks[pos].kind != tokIdent {
				return nil, fail("@filter op %s takes no value, got %q", opTok.text, toks[pos].text)
			}
		} else {
			if pos >= len(toks) {
				return nil, fail("@filter op %s wants exactly one literal ('string'/integer), got end of payload", opTok.text)
			}
			litTok := toks[pos]
			pos++
			switch litTok.kind {
			case tokString:
				f.Literal, f.Quoted = litTok.text, true
			case tokInt:
				if _, perr := strconv.ParseInt(litTok.text, 10, 64); perr != nil {
					return nil, fail("@filter integer literal %q out of range", litTok.text)
				}
				f.Literal = litTok.text
			default:
				return nil, fail("@filter op %s wants exactly one literal ('string'/integer), got %q", opTok.text, litTok.text)
			}
		}
		seenColumn[f.Column] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fail(`@filter wants "<column> <op> [literal] ...", got %q`, t.Payload)
	}
	return out, nil
}

// filterEcho 把解析后的固定条件重建为规范文本,用于生成文件头部的策略来源回显。
func filterEcho(filters []filterDirective) string {
	parts := make([]string, len(filters))
	for i, f := range filters {
		if f.Literal == "" {
			parts[i] = f.Column + " " + f.Op
			continue
		}
		if f.Quoted {
			parts[i] = fmt.Sprintf("%s %s '%s'", f.Column, f.Op, f.Literal)
		} else {
			parts[i] = f.Column + " " + f.Op + " " + f.Literal
		}
	}
	return strings.Join(parts, " ")
}
