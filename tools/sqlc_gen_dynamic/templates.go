package main

// templates.go:生成源码的静态骨架。
//
// 这里的每一段都是逐字节的产物文本(占位符 {{name}} 除外),不做逻辑判断;
// 逐列 / 逐 op 的差异由各 gen 文件中的片段函数与 printer 写入。骨架内的
// 缩进不参与生成契约(产物统一经 go/format),保持与产物一致的缩进只是为了
// 生成失败时转储的原始源码仍然可读。

// ---- 游标取值 ----

const cursorValuesSkel = `// {{fn}}CursorValues 提取用于构造 nextCursor 的排序列值,顺序与有效排序一致。
//
// 参数:
//   - row: 查询结果中的一行模型。
//   - seq: 排序列及方向,顺序与游标值顺序一致。
//
// 返回值:
//   - []any: 与 seq 一一对应的列值切片。
func {{fn}}CursorValues(row {{model}}, seq []dynq.SortField) []any {
	vals := make([]any, len(seq))
	for i, sf := range seq {
		switch sf.Key {
{{cases}}		default:
			vals[i] = nil
		}
	}
	return vals
}
`

// ---- List ----

const listSkel = `// List{{res}} 按 params 的过滤、排序、投影与分页条件查询列表页。
// 返回本页行、是否还有下一页与续翻游标;total 由 Count{{res}} 单独计算
// (非强一致快照)。生成代码为纯函数,不持有连接状态。
// 未查询列在结果中留零值(动态投影固有语义),需区分时按请求 fields 做 DTO 化。
//
// 参数:
//   - ctx:    查询上下文。
//   - db:     数据库连接,满足 DBTX 接口即可。
//   - params: typed 查询参数,可由 Parse{{res}}Query 产出或直接构造。
//   - opts:   运行期能力收窄选项;禁用目标不在 registry 时返回错误。
//
// 返回值:
//   - dynq.Result[{{model}}]: 本页行、是否还有下一页与续翻游标。
//   - error:              查询或校验失败时的错误。
func List{{res}}(ctx context.Context, db {{dbtx}}, params List{{res}}Params, opts ...dynq.QueryOption) (dynq.Result[{{model}}], error) {
	cfg, err := dynq.NewQueryConfig({{reg}}Registry, opts...)
	if err != nil {
		return dynq.Result[{{model}}]{}, err
	}
	if err := validate{{res}}Params(params, cfg); err != nil {
		return dynq.Result[{{model}}]{}, err
	}
	limit, err := dynq.CheckPagination({{reg}}Registry, params.Limit, params.Offset, params.After)
	if err != nil {
		return dynq.Result[{{model}}]{}, err
	}
	// 解析有效排序:未指定排序时回落表级 @default_sort。
	seq, err := dynq.ResolveSort({{reg}}Registry, cfg, params.OrderBy)
	if err != nil {
		return dynq.Result[{{model}}]{}, err
	}
	fp := dynq.CursorFingerprint({{reg}}Registry.Resource, seq)
	// 计算 SELECT 列:投影列并集排序列(排序所需列始终参与查询)。
	cols := dynq.SelectColumns({{reg}}Registry, params.SelectFields, seq)
	b := sq.Select(cols...).From({{table}}).PlaceholderFormat(sq.Dollar).Where(sq.And(build{{res}}Where(params))).
		OrderBy(dynq.OrderBySQL(seq)).Limit(uint64(limit) + 1)
	// After 非 nil 时为游标模式:解码并校验游标,从该位置继续读取。
	if params.After != nil {
		vals, err := dynq.DecodeCursor(*params.After, fp, seq)
		if err != nil {
			return dynq.Result[{{model}}]{}, err
		}
		seek, err := dynq.SeekPredicate(seq, vals)
		if err != nil {
			return dynq.Result[{{model}}]{}, err
		}
		b = b.Where(seek)
	}
	if params.Offset > 0 {
		b = b.Offset(uint64(params.Offset))
	}
	query, args, err := b.ToSql()
	if err != nil {
		return dynq.Result[{{model}}]{}, err
	}
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return dynq.Result[{{model}}]{}, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[{{model}}])
	if err != nil {
		return dynq.Result[{{model}}]{}, err
	}
	// 以 LIMIT n+1 探测是否还有下一页,并构造 nextCursor。
	hasMore := len(items) > int(limit)
	if hasMore {
		items = items[:limit]
	}
	var next string
	if hasMore {
		next, err = dynq.EncodeCursor(fp, seq, {{fn}}CursorValues(items[len(items)-1], seq))
		if err != nil {
			return dynq.Result[{{model}}]{}, err
		}
	}
	return dynq.Result[{{model}}]{Items: items, HasMore: hasMore, NextCursor: next}, nil
}
`

// ---- Count ----

const countSkel = `// Count{{res}} 统计满足过滤条件的行数,与分页模式和游标位置无关;
// 忽略投影、排序与游标。
//
// 参数:
//   - ctx:    查询上下文。
//   - db:     数据库连接,满足 DBTX 接口即可。
//   - params: typed 查询参数,与 List{{res}} 使用同一份形态复核。
//   - opts:   运行期能力收窄选项。
//
// 返回值:
//   - int64: 满足过滤条件的行数。
//   - error: 查询或校验失败时返回错误。
func Count{{res}}(ctx context.Context, db {{dbtx}}, params List{{res}}Params, opts ...dynq.QueryOption) (int64, error) {
	cfg, err := dynq.NewQueryConfig({{reg}}Registry, opts...)
	if err != nil {
		return 0, err
	}
	if err := validate{{res}}Params(params, cfg); err != nil {
		return 0, err
	}
	if _, err := dynq.CheckPagination({{reg}}Registry, params.Limit, params.Offset, params.After); err != nil {
		return 0, err
	}
	query, args, err := sq.Select("COUNT(*)").From({{table}}).PlaceholderFormat(sq.Dollar).Where(sq.And(build{{res}}Where(params))).ToSql()
	if err != nil {
		return 0, err
	}
	var total int64
	if err := db.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}
`

// ---- Schema ----

const schemaSkel = `// {{res}}QuerySchema 是前端契约出口:返回经 opts 减法投影后的展示字段集合,
// 前端据此渲染查询组件。@internal 字段与被 opts.SelectDisabled 移除的字段不
// 出现;其余字段保留,where / sort 禁用只收窄 ops 与 sortable。
// Schema 是(生成期元数据, opts)的确定性纯函数,同一 opts 产出字节级相同
// 结果,内容 hash 可作 ETag。
//
// 参数:
//   - opts: 运行期能力收窄选项,须与 Parse/List/Count 使用同一份 opts。
//
// 返回值:
//   - dynq.ResourceSchema: 前端渲染所需的能力描述(字段、操作符、排序、分页配置)。
func {{res}}QuerySchema(opts ...dynq.QueryOption) dynq.ResourceSchema {
	cfg, err := dynq.NewQueryConfig({{reg}}Registry, opts...)
	if err != nil {
		// 禁用目标不在 registry 属于编程错误。Parse/List/Count/Schema 四点同参纪律
		// 下,此处 panic 显式暴露;静默吞错会掩盖权限收窄失效。
		panic(err)
	}
	return dynq.BuildSchema({{reg}}Registry, cfg)
}
`

// ---- Parse(协议门) ----

const parseQueryHead = `// Parse{{res}}Query 是协议门:将严格解码后的 JSON 查询信封转换为 typed 查询参数。
// 解析过程为穷举 switch,零反射;排序回落与游标内容校验统一在查询函数执行。
//
// 参数:
//   - q:   查询信封,需先经 dynq.DecodeQuery 严格解码。
//   - opts: 运行期能力收窄选项;禁用目标不在 registry 时返回错误。
//
// 返回值:
//   - List{{res}}Params: 解析后的查询参数,可直接传给 List{{res}}/Count{{res}}。
//   - error:          解析失败时的结构化错误(字段未知、op 越权、条件冲突、类型不符等)。
func Parse{{res}}Query(q *dynq.Query, opts ...dynq.QueryOption) (List{{res}}Params, error) {
	cfg, err := dynq.NewQueryConfig({{reg}}Registry, opts...)
	if err != nil {
		return List{{res}}Params{}, err
	}
	var out List{{res}}Params
`

const parseQueryBody = `	budget := dynq.NewBudget()

	// 解析投影字段:显式空数组、@internal 字段、被禁用字段与未知字段均被拒绝;重复字段去重并保持顺序。
	selectFields, err := dynq.ParseSelectFields(q.Fields, cfg, {{reg}}Registry)
	if err != nil {
		return out, err
	}
	out.SelectFields = selectFields

	// 解析过滤条件:条件之间恒为 AND 语义;条件条数与占位符预算均受上限约束。
	if len(q.Where) > dynq.MaxWhere {
		return out, dynq.ErrTooManyConditions()
	}
	for i, c := range q.Where {
		idx, ok := {{reg}}FieldIndex[dynq.FieldName(c.Field)]
		if !ok {
			return out, dynq.NewErr(dynq.KindWhere, c.Field, "unknown field", i)
		}
		f := {{reg}}Registry.Fields[idx]
		if f.Hidden {
			return out, dynq.NewErr(dynq.KindWhere, c.Field, "field hidden", i)
		}
		if cfg.WhereDisabled(f.Key) {
			return out, dynq.NewErr(dynq.KindWhere, c.Field, "field disabled in this context", i)
		}
		op, err := dynq.ParseOp(c.Op)
		if err != nil {
			return out, dynq.NewErr(dynq.KindWhere, c.Field, "unknown op: "+string(c.Op), i)
		}
		if !f.HasOp(op) {
			return out, dynq.NewErr(dynq.KindWhere, c.Field, "op not allowed: "+string(c.Op), i)
		}
		if err := budget.Add(1); err != nil {
			return out, dynq.AsError(err).At(dynq.KindWhere, i, c.Field)
		}
		if op == dynq.OpIsNull || op == dynq.OpNotNull {
			if c.Value != nil || c.RightValue != nil {
				return out, dynq.NewErr(dynq.KindWhere, c.Field, "is_null/not_null must not carry value or right_value", i)
			}
		}
		switch f.Key {
`

const parseQueryTail = `		}
	}
{{nullGuards}}
	// 解析排序:字段须在注册表内且可排序;方向归一化为 asc/desc,并转为逻辑键。
	for i, s := range q.Sort {
		idx, ok := {{reg}}FieldIndex[dynq.FieldName(s.Field)]
		if !ok {
			return out, dynq.NewErr(dynq.KindSort, s.Field, "unknown field", i)
		}
		f := {{reg}}Registry.Fields[idx]
		if f.Hidden {
			return out, dynq.NewErr(dynq.KindSort, s.Field, "field hidden", i)
		}
		if !f.Sortable {
			return out, dynq.NewErr(dynq.KindSort, s.Field, "field not sortable", i)
		}
		if cfg.SortDisabled(f.Key) {
			return out, dynq.NewErr(dynq.KindSort, s.Field, "field disabled in this context", i)
		}
		desc, err := dynq.NormalizeOrder(s.Order)
		if err != nil {
			return out, dynq.NewErr(dynq.KindSort, s.Field, "invalid order: "+s.Order, i)
		}
		out.OrderBy = append(out.OrderBy, dynq.OrderBy{Field: f.Key, Desc: desc})
	}

	// 解析分页:判定 offset/游标模式并归一化(page→Offset、pageSize→Limit)。
	limit, offset, after, err := dynq.NormalizePagination({{reg}}Registry, q.Pagination)
	if err != nil {
		return out, err
	}
	out.Limit, out.Offset, out.After = limit, offset, after
	return out, nil
}
`

// ---- validate(typed 门共享形态复核) ----

const validateHead = `// validate{{res}}Params 校验 params 在当前 opts 下的合法性,List 与 Count 共用。
// 禁用字段携带非零值、IsNull 与 NotNull 同时为真均显式报错,不做静默剔除{{scopeNote}}
//
// 参数:
//   - params: 待复核的查询参数。
//   - cfg:    经 opts 投影后的运行期配置。
//
// 返回值:
//   - error: 复核通过返回 nil,否则返回结构化错误。
func validate{{res}}Params(params List{{res}}Params, cfg *dynq.QueryConfig) error {
`

const validateTail = `	return dynq.ValidateTypedSelectFields(params.SelectFields, cfg, {{reg}}Registry)
}
`

// ---- buildWhere(List/Count 唯一过滤事实源) ----

const whereHead = `// build{{res}}Where 构建过滤 WHERE 条件,List 与 Count 共用,过滤结果一致。
// 所有值均经占位符参数化,列名与操作符为白名单字面量{{notes}}
//
// 参数:
//   - params: 已通过 validate 形态复核的查询参数。
//
// 返回值:
//   - []sq.Sqlizer: 按 AND 语义组合的过滤条件列表。
func build{{res}}Where(params List{{res}}Params) []sq.Sqlizer {
	var conds []sq.Sqlizer
`

const whereFilterHead = `	// 服务端固定不变量(@filter):生成期声明、协议不可见的恒定过滤条件,
	// 与后续条件 AND;字面量经分词器校验后作为 Go 字面量经占位符传入,
	// 不拼进 SQL 文本,零注入面。
`

const whereScopeHead = `	// 数据权限谓词(@scope):多守卫 OR 组合,与业务条件 AND;列名/操作符/
	// 守卫函数名/字面量参数为生成期白名单字面量,标识符参数经占位符参数化;
	// IN-子查询与不相关标量函数在单条语句内只求值一次;params.Scope 非 nil
	// 已由 validate 保证。
`

const whereTail = `	return conds
}
`

const validateScopeGuard = `	// 数据权限 fail-closed:声明 @scope 的表缺省 Scope 时显式报错,
	// 宁可拒绝服务也不静默退化为全量可见。
	if params.Scope == nil {
		return dynq.ErrScopeRequired()
	}
`

// ---- Table marker 与选项别名 ----

const updateMarkerSkel = `// {{sing}}Table 是本表 Update 方法族的编译期 marker:选项类型按表绑定,
// 跨表误用(SET / WHERE 选项串表)在编译期被拒绝,而不是运行期报错。
type {{sing}}Table struct{}

// {{sing}}Update 是 Update 方法族的命名空间对象(如 {{sing}}Update.SetName)。
// 取 XxxUpdate 而非表名单数:与 sqlc-gen-go 同包时模型类型已占用单数名
// (如 sys_user → SysUser),命名空间对象不能复用。
var {{sing}}Update {{sing}}Table

// {{sing}}UpdateOption 是本表的更新选项别名。SET 资格面是列级 @updatable 的
// set-capable 形态;WHERE 资格面是 locate-capable 形态,有 @pk 时自动附带
// 主键列的完整类型矩阵(定位方法)。两个能力面与 @dynamic / @scope 解耦。
type {{sing}}UpdateOption = dynq.UpdateOption[{{sing}}Table]

`

// ---- SET 方法族 ----

const updateSetHead = `// ---- SET 白名单(set-capable @updatable 列;未调用的列保持原值)----

`

// ---- @filter 绕过 ----

const withoutFilterSkel = `// ---- @filter 绕过 ----

// WithoutFilter 绕过本表的 @filter 不变量(幂等;不豁免零 where 守卫)。
// 典型用途:恢复被软删不变量挡住的历史行;绕过是整表级开关,不做条件细化。
func ({{sing}}Table) WithoutFilter() {{sing}}UpdateOption {
	return func(st *dynq.UpdateState[{{sing}}Table]) error {
		st.BypassFilter()
		return nil
	}
}

`

// ---- WHERE 定位方法族 ----

const updateWhereHead = `// ---- WHERE 定位方法族(@pk 自动矩阵 + locate-capable @updatable)----

`

// ---- UpdateXxx ----

const updateFuncTail = `// Update{{res}} 按 opts 收集的 SET / WHERE 组合更新行,返回实际改写行数。
// SET 白名单与 WHERE 定位条件均来自列级 @updatable;有 @pk 时自动附带
// 主键列的定位方法(类型矩阵)。@scope 不参与写面,权限由读取阶段保证。
// 零 SET 或零调用方 WHERE 条件显式拒绝(Kind=set / where),不发全表 UPDATE,
// 也不静默 no-op。
{{filterDoc}}{{noPkDoc}}//
// 参数:
//   - ctx: 更新上下文。
//   - db:  数据库连接,满足 DBTX 接口即可。
//   - opts: SET / WHERE 选项({{sing}}Update.*),跨表选项编译期不可混用。
//
// 返回值:
//   - int64: 实际改写行数;0 = 未命中(条件不匹配),不隐式特判。
//   - error: 校验或执行失败时的错误。
func Update{{res}}(ctx context.Context, db {{dbtx}}, opts ...{{sing}}UpdateOption) (int64, error) {
	st := dynq.NewUpdateState[{{sing}}Table]()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(st); err != nil {
			return 0, err
		}
	}
	// 空 SET / 零 where 守卫:按调用方条件数计——@filter 叠加后最终 WHERE
	// 恒非空,不得以最终 SQL 形态代替判断。
	if err := st.Validate(); err != nil {
		return 0, err
	}
	conds := make([]sq.Sqlizer, 0, {{cap}})
{{filterConds}}	conds = append(conds, st.Wheres()...)
	b := sq.Update({{table}}).PlaceholderFormat(sq.Dollar).Where(sq.And(conds))
	for _, a := range st.Sets() {
		b = b.Set(a.Column, sq.Expr("?"+a.Cast, a.Value))
	}
	query, args, err := b.ToSql()
	if err != nil {
		return 0, err
	}
	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
`

// ---- UpdateXxxReturning / UpdateXxxReturningByPk ----

const returningByPkHead = `// Update{{res}}ReturningByPk 按 pk 定位更新单行并返回改写后的整行:一条 SQL
// 完成 SET / WHERE / RETURNING,调用方无需更新后回查。
// 主键作为必填定位参数内建到 WHERE(与 opts 条件 AND);动态 WHERE 不保证
// 唯一,因此定位以参数承载——opts 条件是可选收窄,不再要求至少一条 Where。
// 零命中返回 pgx.ErrNoRows。
// 返回行语义:行值为 SET 表达式求值与 BEFORE 行级触发器改写后的新值
// (未 SET 的列保留原值),AFTER 触发器对行的后续修改不回传。
// SET 白名单、@filter 不变量(可 WithoutFilter 绕过)与零 SET 守卫与
// Update{{res}} 一致;@scope 不参与写面,权限由读取阶段保证。
//
// 参数:
//   - ctx: 更新上下文。
//   - db:  数据库连接,满足 DBTX 接口即可。
//   - pk:  {{pkKey}} 列的定位值(必填;零命中即未命中)。
//   - opts: SET / WHERE 选项({{sing}}Update.*),跨表选项编译期不可混用。
//
// 返回值:
//   - {{model}}: 被改写的整行。
//   - error: 校验或执行失败时的错误;零命中为 pgx.ErrNoRows。
func Update{{res}}ReturningByPk(ctx context.Context, db {{dbtx}}, pk {{pkType}}, opts ...{{sing}}UpdateOption) ({{model}}, error) {
	st := dynq.NewUpdateState[{{sing}}Table]()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(st); err != nil {
			return {{model}}{}, err
		}
	}
	// 主键定位内建:计入调用方条件数(Locate 不占槽位,可与 Where{{pkConst}}Eq
	// 等显式收窄并存),零 where 守卫因此天然满足。
	st.Locate(sq.Expr({{locateSQL}}, pk))
	if err := st.Validate(); err != nil {
		return {{model}}{}, err
	}
`

const returningMultiHead = `// Update{{res}}Returning 按 opts 收集的 SET / WHERE 组合更新行并返回被改写的
// 整行:一条 SQL 完成 SET / WHERE / RETURNING,调用方无需更新后回查。
// 命中几行返回几行;返回行序未定义(RETURNING 无 ORDER BY),不得做顺序
// 假设;零命中返回空切片 + nil error。
// 返回行语义:行值为 SET 表达式求值与 BEFORE 行级触发器改写后的新值
// (未 SET 的列保留原值),AFTER 触发器对行的后续修改不回传。
// SET 白名单、@filter 不变量(可 WithoutFilter 绕过)与零 SET / 零 WHERE
// 守卫与 Update{{res}} 一致;@scope 不参与写面,权限由读取阶段保证。
{{noPkDoc}}//
// 参数:
//   - ctx: 更新上下文。
//   - db:  数据库连接,满足 DBTX 接口即可。
//   - opts: SET / WHERE 选项({{sing}}Update.*),跨表选项编译期不可混用。
//
// 返回值:
//   - []{{model}}: 被改写的整行,按命中返回;零命中为空切片。
//   - error:    校验或执行失败时的错误。
func Update{{res}}Returning(ctx context.Context, db {{dbtx}}, opts ...{{sing}}UpdateOption) ([]{{model}}, error) {
	st := dynq.NewUpdateState[{{sing}}Table]()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(st); err != nil {
			return nil, err
		}
	}
	// 空 SET / 零 where 守卫:按调用方条件数计——@filter 叠加后最终 WHERE
	// 恒非空,不得以最终 SQL 形态代替判断。
	if err := st.Validate(); err != nil {
		return nil, err
	}
`

const returningTail = `	conds := make([]sq.Sqlizer, 0, {{cap}})
{{filterConds}}	conds = append(conds, st.Wheres()...)
	b := sq.Update({{table}}).PlaceholderFormat(sq.Dollar).Where(sq.And(conds))
	for _, a := range st.Sets() {
		b = b.Set(a.Column, sq.Expr("?"+a.Cast, a.Value))
	}
	b = b.Suffix({{returning}})
	query, args, err := b.ToSql()
	if err != nil {
		return {{zero}}, err
	}
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return {{zero}}, err
	}
	return {{collect}}(rows, pgx.RowToStructByNameLax[{{model}}])
}
`

// filterCondsHead / filterCondsTail 是 @filter 不变量叠加片段(两条
// RETURNING 入口与 UpdateXxx 共用同一形态)。
const filterCondsHead = `	// 恒定条件:@filter 服务端不变量(WithoutFilter 可绕过),与调用方条件 AND。
	if !st.FilterBypassed() {
`

const filterCondsTail = `	}
`

// ---- 文件头 ----

const headerPrefix = `// Code generated by sqlc_gen_dynamic. DO NOT EDIT.
//
// 策略来源(DDL 注释 DSL;预设展开为 op,隐式 sortable 与 default_ops 授予的 op 不回显):
`

const headerSuffix = `//
// 逻辑键(camelCase)是对外契约;snake_case 物理列名仅在本文件内部使用。
package {{pkg}}

`

// ---- registry 与字段常量 ----

const registryDoc = `// {{reg}}Registry 是本表的生成期元数据:协议校验白名单、typed 查询参数与前端
// schema 均由该元数据生成,三者由构造保证一致。运行期能力为 registry 能力
// 减去 opts 禁用项(纯减法,只能禁用不能开启)。
var {{reg}}Registry = dynq.TableMeta{
	Resource: {{resource}},
	Table:    {{table}},
	Fields: []dynq.FieldMeta{
`

const fieldIndexDoc = `// {{reg}}FieldIndex 是逻辑字段名到 registry 字段下标的映射,供白名单校验使用。
var {{reg}}FieldIndex = map[dynq.FieldName]int{
`

const enumValsDoc = `
// {{reg}}{{const}}EnumVals 是 {{key}} 字段的合法枚举值白名单,供类型收敛校验使用。
var {{reg}}{{const}}EnumVals = []string{{list}}
`

const fieldConstSkel = `// {{sing}}Field 提供逻辑字段常量,供禁用选项、OrderBy 与 SelectFields 引用。
// 字段名拼写错误将在编译期被捕获;@internal 列不提供常量。
var {{sing}}Field = struct {
{{decls}}}{
{{vals}}}
`

// ---- Params ----

const scopeStructSkel = `// {{res}}Scope 是 @scope 守卫函数的参数载体:字段为全部守卫声明的标识符参数并集
// (字面量参数已内联进谓词,不占用字段)。协议门结构性无法设置该字段;
// 缺省时 List/Count fail-closed 报错(Kind=scope),不静默退化为全量可见。
type {{res}}Scope struct {
{{decls}}}

`

const paramsHead = `// List{{res}}Params 是 typed 门的查询参数,供内部调用方直接构造(编译期安全)。
// 每个字段以单值承载一个操作符,因此无法表达协议门可拒绝的条件冲突——
// 冲突检测仅在协议门执行。
//
// 各操作符字段的零值语义:
//   - 指针/切片字段为 nil 或空时,该条件不参与过滤;
//   - 模糊匹配字段为零值 "" 时,该条件不参与过滤;
//   - IsNull/NotNull 为 false 时不参与,二者同时为 true 非法。
type List{{res}}Params struct {
`

const paramsScopeFields = `	// Scope 是数据权限上下文(表级 @scope 声明的资源必填,fail-closed):
	// 与业务槽位"零值=条件不参与"不同,Scope 为 nil 时 List/Count 直接报错。
	// 协议门(Parse)无法设置该字段——客户端请求不构成注入通道;
	// 注入方式:params.Scope = dynq.ScopeFromContext[{{res}}Scope](ctx) 或直接构造。
	Scope *{{res}}Scope
`

const paramsTail = `	// OrderBy 指定排序(逻辑字段名与方向);为 nil 时回落表级 @default_sort。
	OrderBy []dynq.OrderBy
	// SelectFields 指定动态投影列(逻辑键);为 nil 时默认全量投影(除 @internal 列)。
	SelectFields []dynq.FieldName
	// Limit 为 offset 模式的每页条数,为 0 时使用表级默认 pageSize。
	Limit  uint32
	// Offset 为 offset 模式的偏移量,仅在 offset 模式生效。
	Offset uint32
	// After 为游标模式的不透明续翻凭证;为 nil 时使用 offset 模式,与 Offset>0 互斥。
	After *string
}
`
