package main

import (
	"fmt"
	"strconv"
	"strings"
)

// 注释 DSL(§5.2/§5.3):指令必须包裹在 [ ] 中且内容以 @ 起始;
// 不以 @ 起始的方括号是 label 字面文本(人类注释方括号不误伤)。
//
// 列级:<label> [@dynamic: <项,项,...>] [@sortable] [@hidden] [@selectable]
//       [@api: <endpoint> value_key=<顶层字段名> label_key=<顶层字段名> [mode=<single|multi|search>]]
// 表级:[@default_sort: <字段> <ASC|DESC>] [@page: <默认>/<上限>] [@pk: <列>]

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
		close := strings.IndexByte(rest[open:], ']')
		if close < 0 {
			return "", nil, fail("unclosed '[' in comment %q", comment)
		}
		close += open
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
		allowed = map[string]bool{"default_sort": true, "page": true, "pk": true}
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
