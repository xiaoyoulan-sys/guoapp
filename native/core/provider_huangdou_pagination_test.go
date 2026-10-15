package core

import "testing"

func TestHuangdouCatalogPagination(t *testing.T) {
	tests := []struct {
		name    string
		data    map[string]any
		page    int
		rows    int
		more    bool
		wantErr bool
	}{
		{name: "total", data: map[string]any{"page": 2, "page_size": 30, "total": 61}, page: 2, rows: 30, more: true},
		{name: "last total page", data: map[string]any{"page": 3, "page_size": 30, "total": 61}, page: 3, rows: 1},
		{name: "explicit end", data: map[string]any{"page": 2, "has_more": false}, page: 2, rows: 30},
		{name: "camel case explicit end", data: map[string]any{"page": 2, "hasMore": false}, page: 2, rows: 30},
		{name: "wrong page", data: map[string]any{"page": 1, "has_more": true}, page: 2, rows: 30, wantErr: true},
		{name: "empty continuation", data: map[string]any{"page": 2, "has_more": true}, page: 2, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			more, err := huangdouCatalogHasMore(map[string]any{"data": test.data}, test.page, test.rows)
			if (err != nil) != test.wantErr || more != test.more {
				t.Fatalf("more=%t err=%v", more, err)
			}
		})
	}
}
