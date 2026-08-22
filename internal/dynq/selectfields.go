package dynq

// ParseSelectFields 解析协议门的 fields 数组:
//   - nil(缺省)= 默认全量投影(所有非 @hidden 列),由 SelectColumns 体现;
//   - 显式空数组 → 拒绝(与缺省语义二分);
//   - 仅可点名白名单内字段:未知 → 400;@hidden → 400;select 域禁用 → 400;
//   - 重复元素去重保序。
func ParseSelectFields(raw []string, cfg *QueryConfig, meta TableMeta) ([]FieldName, error) {
	if raw == nil {
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, NewErr(KindFields, "", "fields must not be empty (omit to select all)", -1)
	}
	seen := make(map[FieldName]struct{}, len(raw))
	var out []FieldName
	for i, key := range raw {
		f, ok := meta.Field(FieldName(key))
		if !ok {
			return nil, NewErr(KindFields, key, "unknown field", i)
		}
		if f.Hidden {
			return nil, NewErr(KindFields, key, "field hidden", i)
		}
		if cfg.SelectDisabled(f.Key) {
			return nil, NewErr(KindFields, key, "field disabled in this context", i)
		}
		if _, dup := seen[f.Key]; dup {
			continue
		}
		seen[f.Key] = struct{}{}
		out = append(out, f.Key)
	}
	return out, nil
}

// SelectColumns 构造实际投影(投影并集不变量):
//
//	(SelectFields 映射的物理列;nil → 全部非 hidden 列) ∪ 有效排序列(含 tiebreaker)
//
// 排序列无条件强制并入,保证游标构造与 has_more 探测在任何 fields 收窄下都成立;
// sortable 蕴含非 hidden,故并入集不可能含敏感列。列名全部来自白名单。
func SelectColumns(meta TableMeta, selectFields []FieldName, seq []SortField) []string {
	var cols []string
	if selectFields == nil {
		for _, f := range meta.Fields {
			if !f.Hidden {
				cols = append(cols, f.Column)
			}
		}
	} else {
		seen := make(map[FieldName]struct{}, len(selectFields))
		for _, key := range selectFields {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if f, ok := meta.Field(key); ok && !f.Hidden {
				cols = append(cols, f.Column)
			}
		}
	}
	// 有效排序列(含 tiebreaker)强制并入,去重保序。
	included := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		included[c] = struct{}{}
	}
	for _, sf := range seq {
		if _, ok := included[sf.Column]; !ok {
			included[sf.Column] = struct{}{}
			cols = append(cols, sf.Column)
		}
	}
	return cols
}

// ValidateTypedSelectFields 是 typed 门的等价校验:SelectFields 含禁用、@hidden
// 或未知字段 → error。
func ValidateTypedSelectFields(selectFields []FieldName, cfg *QueryConfig, meta TableMeta) error {
	for i, key := range selectFields {
		f, ok := meta.Field(key)
		if !ok {
			return NewErr(KindFields, string(key), "unknown field", i)
		}
		if f.Hidden {
			return NewErr(KindFields, string(key), "field hidden", i)
		}
		if cfg.SelectDisabled(f.Key) {
			return NewErr(KindFields, string(key), "field disabled in this context", i)
		}
	}
	return nil
}
