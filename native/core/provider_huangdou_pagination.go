package core

import "errors"

func huangdouCatalogHasMore(decoded any, page, rowCount int) (bool, error) {
	data := huangdouDataMap(decoded)
	pageSize := 30
	if value, valid := webProviderInteger(firstPresent(data, "page_size", "pageSize", "limit", "per_page", "perPage"), 500); valid && value > 0 {
		pageSize = value
	}
	if value := firstPresent(data, "page", "page_num", "pageNum", "current_page", "currentPage"); value != nil {
		actualPage, valid := webProviderInteger(value, 1000000)
		if !valid || actualPage != page {
			return false, errors.New("黄豆返回了重复或错误的目录页，已停止继续加载")
		}
	}
	hasMore := pageSize > 0 && rowCount >= pageSize
	if total, valid := webProviderInteger(firstPresent(data, "total", "total_count", "totalCount"), 100000000); valid && total >= rowCount {
		hasMore = page < (total+pageSize-1)/pageSize
	}
	if value := firstPresent(data, "has_more", "hasMore"); value != nil {
		more, valid := yeguoFlag(value)
		if !valid {
			return false, errors.New("黄豆返回的分页标记无效，已停止继续加载")
		}
		hasMore = more
	}
	if hasMore && rowCount == 0 {
		return false, errors.New("黄豆未返回应有的目录页，已停止继续加载")
	}
	return hasMore, nil
}
