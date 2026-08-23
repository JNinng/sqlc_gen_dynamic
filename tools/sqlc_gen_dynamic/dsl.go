package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// 注释 DSL(§5.2/§5.3):指令必须包裹在 [ ] 中且内容以 @ 起始;
// 不以 @ 起始的方括号是 label 字面文本(人类注释方括号不误伤)。
//
// 列级:<label> [@dynamic: <项,项,...>] [@sortable] [@hidden] [@selectable]
//       [@api: <endpoint> value_key=<顶层字段名> label_key=<顶层字段名> [mode=<single|multi|search>]]
// 表级:[@default_sort: <字段> <ASC|DESC>] [@page: <默认>/<上限>] [@pk: <列>]
//       [@scope: <列> <op> <函数>(<参数:类型|字面量>,...) ...]

type dslTag struct {
	Name    string
	Payload string
}

// parseComment 解析注释:返回 label 与指令标签序列。
// 未闭合的 [ → 生成期 fail;[ ] 内出现白名单外的 @xxx → fail,定位到标签名。
func parseComment(where, comment string) (label string, tags []dslTag, err error) {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...))
	}
	var lbl strings.Builder
	rest := comment
	for {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			lbl.WriteString(rest)
			break
		}
		rel := findBlockClose(rest[open+1:])
		if rel < 0 {
			return "", nil, fail("unclosed '[' in comment %q", comment)
		}
		close := open + 1 + rel
		lbl.WriteString(rest[:open])
		block := strings.TrimSpace(rest[open+1 : close])
		if strings.HasPrefix(block, "@") {
			blockTags, perr := parseDirectiveBlock(block)
			if perr != nil {
				return "", nil, fail("%v", perr)
			}
			tags = append(tags, blockTags...)
		} else {
			// 非指令块:方括号原样保留为 label 字面文本。
			lbl.WriteString(rest[open : close+1])
		}
		rest = rest[close+1:]
	}
	return strings.TrimSpace(lbl.String()), tags, nil
}

// findBlockClose 返回块终止 ']' 在 s(块内容,不含开括号)中的下标;
// 块内成对的 [ ] 不终止块(如 @scope 类型标记 int[]/varchar[]),
// 仅未配对的 ']' 终止。
func findBlockClose(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
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
func validateTagNames(where, scope string, tags []dslTag) error {
	var allowed map[string]bool
	if scope == "column" {
		allowed = map[string]bool{"dynamic": true, "sortable": true, "hidden": true, "selectable": true, "api": true}
	} else {
		allowed = map[string]bool{"default_sort": true, "page": true, "pk": true, "scope": true}
	}
	for _, t := range tags {
		if !allowed[t.Name] {
			return fmt.Errorf("%s: unknown directive @%s (allowed for %s: %s)",
				where, t.Name, scope, strings.Join(sortedKeys(allowed), ", "))
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, "@"+k)
	}
	// 简单排序保证报错信息稳定。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
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

// parsePageDirective 解析 @page: "<default>/<max>"。
func parsePageDirective(where string, t dslTag) (def, max int, err error) {
	parts := strings.SplitN(t.Payload, "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%s: @page wants \"<default>/<max>\", got %q", where, t.Payload)
	}
	def, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || def < 1 {
		return 0, 0, fmt.Errorf("%s: @page default must be a positive integer, got %q", where, parts[0])
	}
	max, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || max < 1 {
		return 0, 0, fmt.Errorf("%s: @page max must be a positive integer, got %q", where, parts[1])
	}
	if def > max {
		return 0, 0, fmt.Errorf("%s: @page default %d exceeds max %d", where, def, max)
	}
	return def, max, nil
}

// APISpec 是 @api 指令的解析结果(插件侧;dynq.FieldAPI 为运行期形态)。
type APISpec struct {
	Endpoint string
	ValueKey string
	LabelKey string
	Mode     string // single | multi | search;空 = 由 ops 推导
}

// parseAPIDirective 解析 @api: "<endpoint> value_key=<...> label_key=<...> [mode=...]"。
//   - endpoint:相对前端 baseURL 的路径,禁止 '?'(查询参数由前端按协议拼);
//   - value_key / label_key:必填,items 中的顶层字段名(复用 ListXxx 时即模型字段名);
//   - mode:可选,缺省由列 ops 推导(in → multi、fuzzy → search、否则 single)。
func parseAPIDirective(where string, t dslTag) (*APISpec, error) {
	fields := strings.Fields(t.Payload)
	if len(fields) == 0 {
		return nil, fmt.Errorf("%s: @api wants \"<endpoint> value_key=<...> label_key=<...> [mode=...]\"", where)
	}
	spec := &APISpec{Endpoint: fields[0]}
	if strings.ContainsAny(spec.Endpoint, "?") {
		return nil, fmt.Errorf("%s: @api endpoint must not contain a query string, got %q", where, spec.Endpoint)
	}
	if strings.Contains(spec.Endpoint, "=") {
		// 首 token 是 key=value(漏写 endpoint)而非路径。
		return nil, fmt.Errorf("%s: @api wants \"<endpoint> value_key=<...> label_key=<...> [mode=...]\"", where)
	}
	for _, kv := range fields[1:] {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("%s: @api expects key=value parameters, got %q", where, kv)
		}
		switch key {
		case "value_key":
			spec.ValueKey = value
		case "label_key":
			spec.LabelKey = value
		case "mode":
			switch value {
			case "single", "multi", "search":
				spec.Mode = value
			default:
				return nil, fmt.Errorf("%s: @api mode must be single|multi|search, got %q", where, value)
			}
		default:
			return nil, fmt.Errorf("%s: unknown @api parameter %q (want value_key|label_key|mode)", where, key)
		}
	}
	if spec.Endpoint == "" {
		return nil, fmt.Errorf("%s: @api endpoint is required", where)
	}
	if spec.ValueKey == "" || spec.LabelKey == "" {
		return nil, fmt.Errorf("%s: @api value_key and label_key are required", where)
	}
	return spec, nil
}

// parseDefaultSortDirective 解析 @default_sort: "<column> <ASC|DESC>"。
// 目标列为物理列名;存在性、@sortable、非 @hidden 在表级校验。
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

// scopeGuard 是 @scope 声明的一项守卫:目标列、谓词操作符、守卫函数与实参列表。
type scopeGuard struct {
	Column string
	Op     string // in | eq | gt | gte | lt | lte
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
// 刻意排除 neq/not_in——NOT IN 撞上集合含 NULL 时恒空,是权限事故配方。
var scopeOps = map[string]bool{"in": true, "eq": true, "gt": true, "gte": true, "lt": true, "lte": true}

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
//   - op:封闭集合 in|eq|gt|gte|lt|lte(in = 集合守卫,其余为标量比较);
//   - 函数:小写标识符(可带一层 schema 限定);本体不在 sqlc catalog,
//     不做存在性校验(与 @api endpoint 同一信任级别),由部署侧保证;
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
			return nil, fail("@scope op %q not allowed (want one of: eq, gt, gte, in, lt, lte)", opTok.text)
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
