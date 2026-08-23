package dynq

// 分页双模式(§6.5):模式判定由请求显式形态决定,无隐式回退。
//
//	after 缺省或 null → offset 模式(page 默认 1、pageSize)
//	after 为非空字符串 → keyset 游标模式(page 必须缺省或 = 1)
//	after 为空字符串 → 400(空游标非法)

// NormalizePagination 是协议门的分页归一化:page → Offset、pageSize → Limit。
// page ≤ 0 / pageSize 显式提供且 ≤ 0 或超上限 / offset 超上限 / after 与
// page > 1 混用 / after 为空串 → 400。
func NormalizePagination(meta TableMeta, p *Pagination) (limit uint32, offset uint32, after *string, err error) {
	page := 1
	pageSize := meta.PageDefault
	if p != nil {
		if p.Page != nil {
			page = *p.Page
		}
		if p.PageSize != nil {
			pageSize = *p.PageSize
		}
		after = p.After
	}
	if page < 1 {
		return 0, 0, nil, NewErr(KindPagination, "page", "page must be >= 1", -1)
	}
	if pageSize < 1 {
		return 0, 0, nil, NewErr(KindPagination, "pageSize", "pageSize must be >= 1", -1)
	}
	if pageSize > meta.PageMax {
		return 0, 0, nil, NewErr(KindPagination, "pageSize",
			"pageSize exceeds max "+itoa(meta.PageMax), -1)
	}
	if after != nil {
		if *after == "" {
			return 0, 0, nil, NewErr(KindPagination, "after", "after must not be empty string (omit for offset mode)", -1)
		}
		if page > 1 {
			return 0, 0, nil, NewErr(KindPagination, "after",
				"after cursor cannot be combined with page > 1", -1)
		}
	}
	offset = uint32(page-1) * uint32(pageSize)
	if after == nil && offset > MaxOffset {
		return 0, 0, nil, NewErr(KindPagination, "page",
			"offset exceeds limit "+itoa(MaxOffset)+"; narrow filters or use after cursor", -1)
	}
	return uint32(pageSize), offset, after, nil
}

// CheckPagination 是 typed 门的等价复核与归一化:Limit=0 视为采用表默认
// pageSize;Limit 超上限、After 与 Offset > 0 并存、After 为空串、offset 模式
// Offset 超上限 → error。
func CheckPagination(meta TableMeta, limit, offset uint32, after *string) (uint32, error) {
	if after != nil && *after == "" {
		return 0, NewErr(KindPagination, "after", "after must not be empty string (use nil for offset mode)", -1)
	}
	if after != nil && offset > 0 {
		return 0, NewErr(KindPagination, "after", "after cursor cannot be combined with offset", -1)
	}
	if limit == 0 {
		limit = uint32(meta.PageDefault)
	}
	if int(limit) > meta.PageMax {
		return 0, NewErr(KindPagination, "pageSize", "limit exceeds max "+itoa(meta.PageMax), -1)
	}
	if after == nil && offset > MaxOffset {
		return 0, NewErr(KindPagination, "offset",
			"offset exceeds limit "+itoa(MaxOffset)+"; narrow filters or use after cursor", -1)
	}
	return limit, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
