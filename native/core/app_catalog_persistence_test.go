package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type nativeHongguoRequest struct {
	Offset  int    `json:"offset"`
	Session string `json:"session_id"`
	Scene   string `json:"req_scene"`
}

func nativeHongguoRequestForTest(t *testing.T, request *http.Request) nativeHongguoRequest {
	t.Helper()
	var payload nativeHongguoRequest
	if request.URL.Path != "/reading/distribution/category/landpage/v/" || json.NewDecoder(request.Body).Decode(&payload) != nil {
		t.Errorf("unexpected request, only synthetic catalog text is allowed: %s", request.URL.Path)
	}
	return payload
}

func nativeHongguoResponse(request *http.Request, next int, more bool, session string, ids ...string) *http.Response {
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, map[string]any{"series_id": id, "series_title": "合成目录 " + id})
	}
	body, _ := json.Marshal(map[string]any{"data": map[string]any{
		"next_offset": next, "has_more": more, "session_id": session, "video_data": rows,
	}})
	return sourceFixtureResponse(request, http.StatusOK, string(body))
}

func reopenCatalogEngine(t *testing.T, directory string, transport sourceFixtureTransport) *nativeEngine {
	t.Helper()
	engine, err := newNativeEngine(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.downloads.close)
	engine.downloader.client.Transport = transport
	engine.downloader.limiter = newRequestLimiter(3, 0)
	engine.downloader.cfg.Retries = 1
	engine.downloader.cfg.MaxPagesPerSort = 1
	return engine
}

func TestNativeHongguoCursorRestartsPerCatalog(t *testing.T) {
	var calls []nativeHongguoRequest
	var device, install string
	transport := sourceFixtureTransport(func(request *http.Request) (*http.Response, error) {
		payload := nativeHongguoRequestForTest(t, request)
		calls = append(calls, payload)
		if device == "" {
			device, install = request.URL.Query().Get("device_id"), request.URL.Query().Get("iid")
		} else if request.URL.Query().Get("device_id") != device || request.URL.Query().Get("iid") != install {
			t.Error("restart changed the catalog device identity")
		}
		if payload.Offset > 0 && payload.Session != "fixture-"+payload.Scene {
			t.Error("restart lost the feed session", payload)
		}
		base := map[string]int{"default": 1000, "comic_series": 2000, "ai_series": 3000}[payload.Scene]
		position := payload.Offset / 18
		return nativeHongguoResponse(request, payload.Offset+18, true, "fixture-"+payload.Scene,
			strconv.Itoa(base+position+1), strconv.Itoa(base+position+2)), nil
	})
	engine := sourceFixtureEngine(t, transport)
	engine.downloader.cfg.MaxPagesPerSort = 1
	for _, input := range []nativeInput{
		{Source: sourceHongguo, Page: 1},
		{Source: sourceHongguo, Category: "short_play", Page: 1},
		{Source: sourceHongguo, Category: "comic_series", Page: 1},
		{Source: sourceHongguo, Category: "short_play", Page: 2},
	} {
		if result, err := engine.nativeCatalog(context.Background(), input); err != nil || result.Warning != "" {
			t.Fatal("could not save the initial catalogs", err, result.Warning)
		}
	}
	expected, _ := json.Marshal(engine.downloader.hongguoCatalogSnapshot())
	engine = reopenCatalogEngine(t, engine.directory, transport)
	restored, _ := json.Marshal(engine.downloader.hongguoCatalogSnapshot())
	if !bytes.Equal(expected, restored) {
		t.Fatal("restart lost cursor fields, signatures or device state")
	}
	calls = nil
	for _, input := range []nativeInput{
		{Source: sourceHongguo, Page: 2},
		{Source: sourceHongguo, Category: "short_play", Page: 3},
		{Source: sourceHongguo, Category: "comic_series", Page: 2},
	} {
		if result, err := engine.nativeCatalog(context.Background(), input); err != nil || result.Warning != "" {
			t.Fatal("could not resume the saved catalog", err, result.Warning)
		}
	}
	var offsets []int
	for _, call := range calls {
		offsets = append(offsets, call.Offset)
	}
	if !reflect.DeepEqual(offsets, []int{18, 18, 18, 36, 18}) {
		t.Fatal("total catalog and categories shared or reset their cursors", offsets)
	}
	for key, items := range engine.catalogs {
		seen := map[string]bool{}
		for _, item := range items {
			if seen[item.ID] {
				t.Fatal("overlapping pages duplicated an ID", key, item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(engine.catalogs[sourceHongguo]) != 10 || len(engine.catalogs["hongguo|short_play"]) != 4 {
		t.Fatal("resuming a category dropped entries from the shared library")
	}
	if _, err := engine.nativeCatalog(context.Background(), nativeInput{Source: sourceHongguo, Page: 1, Force: true}); err != nil {
		t.Fatal(err)
	}
	if engine.downloader.hongguoCatalogSnapshot().Feeds["short_play"].Offset != 36 {
		t.Fatal("refreshing an unchanged head reset the saved continuation")
	}
}

func TestNativeHongguoCursorRecoversSessionsAfterRestart(t *testing.T) {
	for _, scenario := range []string{"expired", "rejected", "repeated-page"} {
		t.Run(scenario, func(t *testing.T) {
			var calls []nativeHongguoRequest
			transport := sourceFixtureTransport(func(request *http.Request) (*http.Response, error) {
				payload := nativeHongguoRequestForTest(t, request)
				calls = append(calls, payload)
				if payload.Offset == 0 {
					return nativeHongguoResponse(request, 18, true, "old-session", "700001", "700002"), nil
				}
				if payload.Offset != 18 {
					t.Error("session recovery changed the saved offset", payload.Offset)
				}
				if scenario == "expired" && payload.Session != "" {
					t.Error("reused an expired session")
				}
				if payload.Session != "" {
					if scenario == "rejected" {
						return sourceFixtureResponse(request, 200, `{"code":1001}`), nil
					}
					return nativeHongguoResponse(request, 36, true, "old-session", "700002", "700001"), nil
				}
				return nativeHongguoResponse(request, 36, true, "new-session", "700002", "700003"), nil
			})
			engine := sourceFixtureEngine(t, transport)
			engine.downloader.cfg.MaxPagesPerSort = 1
			input := nativeInput{Source: sourceHongguo, Category: "ai_series", Page: 1}
			if result, err := engine.nativeCatalog(context.Background(), input); err != nil || result.Warning != "" {
				t.Fatal(err, result.Warning)
			}
			if scenario == "expired" {
				engine.mu.Lock()
				cursor := engine.hongguoCatalog.Feeds["category:ai_series"]
				cursor.UpdatedAt = time.Now().Add(-time.Hour)
				engine.hongguoCatalog.Feeds["category:ai_series"] = cursor
				err := engine.writeCatalogDiskLocked()
				engine.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			engine = reopenCatalogEngine(t, engine.directory, transport)
			input.Page = 2
			if result, err := engine.nativeCatalog(context.Background(), input); err != nil || result.Warning != "" {
				t.Fatal("failed to recover a saved session", err, result.Warning)
			}
			wantCalls := 3
			if scenario == "expired" {
				wantCalls = 2
			}
			if len(calls) != wantCalls || calls[len(calls)-1].Session != "" {
				t.Fatal("session recovery was not bounded", calls)
			}
			restarted := reopenCatalogEngine(t, engine.directory, transport)
			cursor := restarted.downloader.hongguoCatalogSnapshot().Feeds["category:ai_series"]
			if cursor.Offset != 36 || cursor.SessionID != "new-session" || len(restarted.catalogs["hongguo|ai_series"]) != 3 {
				t.Fatal("recovered items and cursor were not saved together")
			}
		})
	}
}

func TestNativeHongguoStalledCursorKeepsPartialItems(t *testing.T) {
	var calls []int
	transport := sourceFixtureTransport(func(request *http.Request) (*http.Response, error) {
		payload := nativeHongguoRequestForTest(t, request)
		calls = append(calls, payload.Offset)
		if payload.Offset == 0 {
			return nativeHongguoResponse(request, 18, true, "fixture", "700001"), nil
		}
		return nativeHongguoResponse(request, 18, true, "fixture", "700002"), nil
	})
	engine := sourceFixtureEngine(t, transport)
	engine.downloader.cfg.MaxPagesPerSort = 1
	input := nativeInput{Source: sourceHongguo, Category: "ai_series", Page: 1}
	if _, err := engine.nativeCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.Page = 2
	result, err := engine.nativeCatalog(context.Background(), input)
	if err != nil || result.Warning == "" || result.Fresh || !reflect.DeepEqual(calls, []int{0, 18, 18}) {
		t.Fatal("stalled pagination was accepted or retried indefinitely", err, result.Warning, calls)
	}
	restarted := reopenCatalogEngine(t, engine.directory, transport)
	cached := restarted.nativeCached("hongguo|ai_series")
	cursor := restarted.downloader.hongguoCatalogSnapshot().Feeds["category:ai_series"]
	if cursor.Offset != 18 || len(cached.Items) != 2 || cached.Fresh || cached.Warning == "" {
		t.Fatal("restart lost the last valid cursor, partial data or failure warning")
	}
}

func TestNativeCatalogPreservesLargeLibraryAndPage501(t *testing.T) {
	var requested string
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/videos/category/ai-duanju" {
			return nil, errors.New("only synthetic catalog metadata is allowed")
		}
		requested = request.URL.Query().Get("page")
		return sourceFixtureResponse(request, 200, `{"data":[{"id":"6001","title":"第6001部"}]}`), nil
	})
	items := make([]nativeDrama, 6000)
	for index := range items {
		items[index] = nativeDrama{ID: fmt.Sprintf("huangguoai:%d", index+1), Source: sourceHuangguoAI, Title: "合成目录"}
	}
	key := nativeCatalogKey(sourceHuangguoAI, "ai-duanju")
	if err := engine.saveCatalogCache(key, &nativeCatalogResult{Items: items, Page: 500, HasMore: true}); err != nil {
		t.Fatal(err)
	}
	if result, err := engine.nativeCatalog(context.Background(), nativeInput{Source: sourceHuangguoAI, Category: "ai-duanju", Page: 501}); err != nil || result.Warning != "" {
		t.Fatal(err, result.Warning)
	}
	if requested != "501" {
		t.Fatal("catalog silently clamped the requested page", requested)
	}
	if err := engine.saveCatalogCache(nativeCatalogKey(sourceHuangguoAI, "ai-manju"), &nativeCatalogResult{
		Items: []nativeDrama{{ID: "huangguoai:6001", Source: sourceHuangguoAI, Title: "第6001部"}, {ID: "huangguoai:6002", Source: sourceHuangguoAI, Title: "第6002部"}},
		Page:  1,
	}); err != nil {
		t.Fatal(err)
	}
	restarted := reopenCatalogEngine(t, engine.directory, nil)
	cached := restarted.nativeCached(key)
	if len(cached.Items) != 6001 || cached.Page != 501 || cached.HasMore || len(restarted.catalogs[sourceHuangguoAI]) != 6002 {
		t.Fatal("restart truncated data, advanced past lost IDs or duplicated category entries", len(cached.Items), cached.Page)
	}
}

func TestNativeCatalogSaveFailureRetriesWithoutAdvancing(t *testing.T) {
	var offsets []int
	transport := sourceFixtureTransport(func(request *http.Request) (*http.Response, error) {
		payload := nativeHongguoRequestForTest(t, request)
		offsets = append(offsets, payload.Offset)
		position := payload.Offset / 18
		return nativeHongguoResponse(request, payload.Offset+18, true, "fixture",
			strconv.Itoa(1001+position), strconv.Itoa(1002+position)), nil
	})
	engine := sourceFixtureEngine(t, transport)
	engine.downloader.cfg.MaxPagesPerSort = 1
	input := nativeInput{Source: sourceHongguo, Category: "short_play", Page: 1}
	if _, err := engine.nativeCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(engine.directory, "catalogs.json")
	original, _ := os.ReadFile(path)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	input.Page = 2
	result, err := engine.nativeCatalog(context.Background(), input)
	if err != nil || result.Warning == "" || result.Fresh || result.saveError == nil {
		t.Fatal("save failure was reported as a fresh catalog", err, result.Warning)
	}
	if len(engine.nativeCached("hongguo|short_play").Items) != 3 {
		t.Fatal("save failure discarded usable memory data")
	}
	input.Page = 3
	if result, err := engine.nativeCatalog(context.Background(), input); err != nil || result.Warning == "" || len(offsets) != 2 {
		t.Fatal("an unsaved catalog advanced to another page", err, offsets)
	}
	backup, _ := os.ReadFile(path + ".saved")
	if !bytes.Equal(original, backup) {
		t.Fatal("failed writes changed the previous persisted catalog")
	}
	if err := os.Rename(path, path+".blocked"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	restarted := reopenCatalogEngine(t, engine.directory, transport)
	if cursor := restarted.downloader.hongguoCatalogSnapshot().Feeds["category:short_play"]; cursor.Offset != 18 || len(restarted.catalogs["hongguo|short_play"]) != 2 {
		t.Fatal("restart resumed an offset whose data had not been saved")
	}
	if status := restarted.sourceStatus(sourceHongguo); status.Stage != "上次保存未完成" || restarted.nativeCached("hongguo|short_play").Fresh {
		t.Fatal("restart hid the interrupted save", status)
	}
	status, err := engine.startSourceTask(sourceHongguo, "retrySave", nativeDrama{})
	if err != nil || status.StorageError != "" || len(offsets) != 2 {
		t.Fatal("save retry failed or issued network requests", err, status)
	}
	restarted = reopenCatalogEngine(t, engine.directory, transport)
	if len(restarted.catalogs["hongguo|short_play"]) != 3 || restarted.downloader.hongguoCatalogSnapshot().Feeds["category:short_play"].Offset != 36 {
		t.Fatal("retry did not persist pending items with their cursor")
	}
	if result, err := restarted.nativeCatalog(context.Background(), input); err != nil || result.Warning != "" || !reflect.DeepEqual(offsets, []int{0, 18, 36}) {
		t.Fatal("continuation did not resume after the saved page", err, offsets)
	}
}

func TestNativeCatalogSizeLimitPreservesPreviousFile(t *testing.T) {
	var requests atomic.Int32
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("network forbidden")
	})
	if err := engine.saveCatalogCache(sourceHongguo, &nativeCatalogResult{Items: []nativeDrama{{ID: "hongguo:1", Title: "已保存"}}, Page: 1, HasMore: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(engine.directory, "catalogs.json")
	before, _ := os.ReadFile(path)
	result := nativeCatalogResult{Items: []nativeDrama{{ID: "hongguo:2", Title: "未保存", Description: strings.Repeat("x", nativeCatalogMaxBytes)}}, Page: 2, HasMore: true}
	err := engine.saveCatalogCache(sourceHongguo, &result)
	if !errors.Is(err, errNativeCatalogLimit) || result.Fresh || !strings.Contains(result.Warning, "上限") {
		t.Fatal("oversized data was silently accepted", err, result.Warning)
	}
	if _, err := engine.nativeCatalog(context.Background(), nativeInput{Source: sourceHongguo, Page: 3}); err != nil || requests.Load() != 0 {
		t.Fatal("size limit did not stop additional paging", err, requests.Load())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || len(engine.nativeCached(sourceHongguo).Items) != 2 {
		t.Fatal("size limit lost either the previous disk data or pending memory data")
	}
	restarted := reopenCatalogEngine(t, engine.directory, nil)
	if cached := restarted.nativeCached(sourceHongguo); len(cached.Items) != 1 || cached.Page != 1 {
		t.Fatal("restart skipped the last durable catalog")
	}
}

func TestNativeCatalogMetadataSaveExcludesInFlightCursor(t *testing.T) {
	paused, release := make(chan struct{}), make(chan struct{})
	transport := sourceFixtureTransport(func(request *http.Request) (*http.Response, error) {
		payload := nativeHongguoRequestForTest(t, request)
		if payload.Offset == 18 && payload.Scene == "comic_series" {
			close(paused)
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		base := map[string]int{"default": 1000, "comic_series": 2000, "ai_series": 3000}[payload.Scene]
		return nativeHongguoResponse(request, payload.Offset+18, true, "fixture", strconv.Itoa(base+payload.Offset)), nil
	})
	engine := sourceFixtureEngine(t, transport)
	engine.downloader.cfg.MaxPagesPerSort = 1
	if _, err := engine.nativeCatalog(context.Background(), nativeInput{Source: sourceHongguo, Page: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := engine.nativeCatalog(ctx, nativeInput{Source: sourceHongguo, Page: 2})
		if err == nil && result.Warning != "" {
			err = errors.New(result.Warning)
		}
		done <- err
	}()
	select {
	case <-paused:
	case <-ctx.Done():
		t.Fatal("catalog never reached the in-flight checkpoint")
	}
	engine.saveDetailMetadata(nativeDrama{ID: "hongguo:1000", Source: sourceHongguo, Title: "补齐名称"})
	restarted := reopenCatalogEngine(t, engine.directory, transport)
	if cursor := restarted.downloader.hongguoCatalogSnapshot().Feeds["short_play"]; cursor.Offset != 18 {
		t.Error("an unrelated metadata write persisted an uncommitted feed cursor")
	}
	if len(restarted.catalogs[sourceHongguo]) != 3 {
		t.Error("in-flight catalog data was partially persisted")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	restarted = reopenCatalogEngine(t, engine.directory, transport)
	if restarted.downloader.hongguoCatalogSnapshot().Feeds["short_play"].Offset != 36 || len(restarted.catalogs[sourceHongguo]) != 6 {
		t.Fatal("completed page was not saved with its cursor")
	}
}

func TestNativeSourceRecordSaveFailureAndRetryDuringBackoff(t *testing.T) {
	var requests atomic.Int32
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("network forbidden")
	})
	engine.changeSourceRecord(sourceHongguo, func(record *nativeSourceRecord) {
		record.Stage = "已完成"
		record.Health = &nativeSourceHealth{State: "ok", Sample: "原诊断"}
	})
	path := filepath.Join(engine.directory, "sources.json")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	engine.changeSourceRecord(sourceHongguo, func(record *nativeSourceRecord) {
		record.RetryAt = time.Now().Add(time.Minute)
		record.Health = &nativeSourceHealth{State: "catalogOnly", Sample: "新诊断"}
	})
	status, err := engine.startSourceTask(sourceHongguo, "retrySave", nativeDrama{})
	if err != nil || status.StorageError == "" || status.Stage != "等待保存" || status.Health.Sample != "新诊断" || requests.Load() != 0 {
		t.Fatal("failed task persistence was hidden or blocked by network cooldown", err, status)
	}
	if err := os.Rename(path, path+".blocked"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	status, err = engine.startSourceTask(sourceHongguo, "retrySave", nativeDrama{})
	if err != nil || status.StorageError != "" || status.Stage != "已完成" || requests.Load() != 0 {
		t.Fatal("task record retry did not recover", err, status)
	}
	restarted := reopenCatalogEngine(t, engine.directory, nil)
	if restarted.sourceStatus(sourceHongguo).Health.Sample != "新诊断" {
		t.Fatal("retry did not save the new health report")
	}
	engine.mu.Lock()
	record := engine.sourceRecords[sourceHongguo]
	record.Stage = strings.Repeat("x", nativeSourceMaxBytes)
	engine.sourceRecords[sourceHongguo] = record
	err = engine.saveSourceRecordsLocked()
	engine.mu.Unlock()
	if !errors.Is(err, errNativeSourceLimit) || engine.sourceStatus(sourceHongguo).StorageError == "" {
		t.Fatal("oversized task records were silently dropped", err)
	}
	restarted = reopenCatalogEngine(t, engine.directory, nil)
	if restarted.sourceStatus(sourceHongguo).Health.Sample != "新诊断" {
		t.Fatal("oversized records replaced the previous saved report")
	}
}

func TestNativeHongguoAutoLoadsAllBatches(t *testing.T) {
	var calls atomic.Int32
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		payload := nativeHongguoRequestForTest(t, request)
		calls.Add(1)
		base := map[string]int{"default": 1000, "comic_series": 2000, "ai_series": 3000}[payload.Scene]
		return nativeHongguoResponse(request, payload.Offset+18, payload.Offset < 36, "fixture-"+payload.Scene, strconv.Itoa(base+payload.Offset)), nil
	})
	engine.downloader.cfg.MaxPagesPerSort = 1
	if _, err := engine.startSourceTask(sourceHongguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	status := awaitSourceTask(t, engine, sourceHongguo)
	if status.HasMore || status.Count != 9 || status.Error != "" || calls.Load() != 9 {
		t.Fatal("automatic loading stopped before final batch", status, calls.Load())
	}
	if status, err := engine.startSourceTask(sourceHongguo, "auto", nativeDrama{}); err != nil || status.Running {
		t.Fatal("exhausted catalog restarted", status, err)
	}
}

func TestNativeHongguoRefreshKeepsExhaustedCursor(t *testing.T) {
	var newer atomic.Bool
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		payload := nativeHongguoRequestForTest(t, request)
		if !newer.Load() {
			return nativeHongguoResponse(request, 18, false, "fixture", "1001"), nil
		}
		if payload.Offset == 0 {
			return nativeHongguoResponse(request, 18, true, "fixture", "1002"), nil
		}
		return nativeHongguoResponse(request, 36, false, "fixture", "1001"), nil
	})
	engine.downloader.cfg.MaxPagesPerSort = 3
	input := nativeInput{Source: sourceHongguo, Category: "short_play", Page: 1, Force: true}
	if _, err := engine.nativeCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	newer.Store(true)
	if _, err := engine.nativeCatalog(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	cursor := engine.downloader.hongguoCatalogSnapshot().Feeds["category:short_play"]
	items := engine.nativeCached("hongguo|short_play")
	if !cursor.Exhausted || !cursor.Initialized || len(items.Items) != 2 || items.HasMore {
		t.Fatal("refresh reopened an exhausted feed or lost its new item", cursor, items)
	}
}

func TestNativeSourceAutoLoadsAllBatches(t *testing.T) {
	previousBuild := buildAllSources
	buildAllSources = "true"
	defer func() { buildAllSources = previousBuild }()
	var pages []string
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.yeguo.test" || request.URL.Path != "/api/theater/exploreList" {
			t.Fatalf("unexpected automatic catalog request: %s", request.URL.String())
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		page := request.Form.Get("page")
		pages = append(pages, page)
		body := fmt.Sprintf(
			`{"status":"1","data":{"list":[{"video_id":"80%s","title":"自动分页%s","description":"完整资料","episode_count":"6"}],"page":%s,"limit":20,"total":3,"has_more":%q}}`,
			page, page, page, map[bool]string{true: "1", false: "0"}[page != "3"],
		)
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	engine.downloader.yeguoClient().access = &yeguoAccess{
		base: "https://api.yeguo.test", identifier: "auto-fixture", loadedAt: time.Now(),
	}
	if _, err := engine.startSourceTask(sourceYeguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	status := awaitSourceTask(t, engine, sourceYeguo)
	if strings.Join(pages, ",") != "1,2,3" || status.HasMore || status.Count != 3 || status.Error != "" {
		t.Fatal("non-Hongguo source did not automatically load all pages", pages, status)
	}
	if status, err := engine.startSourceTask(sourceYeguo, "auto", nativeDrama{}); err != nil ||
		status.Running || len(pages) != 3 {
		t.Fatal("exhausted source restarted automatically", status, err, pages)
	}
}

func TestNativeSourceAutoStopsAtRepeatedPage(t *testing.T) {
	previousBuild := buildAllSources
	buildAllSources = "true"
	defer func() { buildAllSources = previousBuild }()
	var pages []string
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		page := request.Form.Get("page")
		pages = append(pages, page)
		body := fmt.Sprintf(`{"status":"1","data":{"list":[{"video_id":"801","title":"重复分页"}],"page":%s,"limit":1,"total":999,"has_more":"1"}}`, page)
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	engine.downloader.yeguoClient().access = &yeguoAccess{
		base: "https://api.yeguo.test", identifier: "repeat-fixture", loadedAt: time.Now(),
	}
	if _, err := engine.startSourceTask(sourceYeguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	status := awaitSourceTask(t, engine, sourceYeguo)
	if strings.Join(pages, ",") != "1,2,2" || status.Count != 1 || status.Page != 2 || status.HasMore || status.Error != "" {
		t.Fatal("automatic source did not stop at a repeated page", pages, status)
	}
}

func TestNativeSourceAutoRecoversTransientRepeatedPage(t *testing.T) {
	previousBuild := buildAllSources
	buildAllSources = "true"
	defer func() { buildAllSources = previousBuild }()
	var pages []string
	secondPageCalls := 0
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		page := request.Form.Get("page")
		pages = append(pages, page)
		id, more := "801", "1"
		if page == "2" {
			secondPageCalls++
			if secondPageCalls == 2 {
				id, more = "802", "0"
			}
		}
		body := fmt.Sprintf(`{"status":"1","data":{"list":[{"video_id":%q,"title":"合成目录"}],"page":%s,"limit":1,"total":2,"has_more":%q}}`, id, page, more)
		return sourceFixtureResponse(request, http.StatusOK, body), nil
	})
	engine.downloader.yeguoClient().access = &yeguoAccess{base: "https://api.yeguo.test", identifier: "repeat-fixture", loadedAt: time.Now()}
	if _, err := engine.startSourceTask(sourceYeguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	status := awaitSourceTask(t, engine, sourceYeguo)
	if strings.Join(pages, ",") != "1,2,2" || status.Count != 2 || status.HasMore || status.Error != "" {
		t.Fatal("transient repeated page incorrectly ended pagination", pages, status)
	}
}

func TestNativeSourceAutoRefreshesExhaustedCatalog(t *testing.T) {
	previousBuild := buildAllSources
	buildAllSources = "true"
	defer func() { buildAllSources = previousBuild }()
	var pages []string
	engine := sourceFixtureEngine(t, func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		page := request.Form.Get("page")
		pages = append(pages, page)
		return sourceFixtureResponse(request, http.StatusOK, `{"status":"1","data":{"list":[{"video_id":"802","title":"新剧"}],"page":1,"limit":1,"total":3,"has_more":"1"}}`), nil
	})
	engine.downloader.yeguoClient().access = &yeguoAccess{base: "https://api.yeguo.test", identifier: "refresh-fixture", loadedAt: time.Now()}
	engine.catalogs[sourceYeguo] = []nativeDrama{{ID: "yeguo:801", Source: sourceYeguo, Title: "旧剧"}}
	engine.catalogStates[sourceYeguo] = nativeCatalogState{Page: 3, HasMore: false, UpdatedAt: time.Now().Add(-nativeSourceRefreshInterval - time.Minute)}
	if _, err := engine.startSourceTask(sourceYeguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	status := awaitSourceTask(t, engine, sourceYeguo)
	if strings.Join(pages, ",") != "1" || status.Count != 2 || status.Page != 3 || status.HasMore || status.Error != "" {
		t.Fatal("exhausted catalog refresh lost prior content or pagination state", pages, status)
	}
	if again, err := engine.startSourceTask(sourceYeguo, "auto", nativeDrama{}); err != nil || again.Running || len(pages) != 1 {
		t.Fatal("fresh catalog restarted automatic refresh", again, err, pages)
	}
}

func TestNativeHongguoAutoStopSurvivesRestart(t *testing.T) {
	started := make(chan struct{}, 1)
	transport := sourceFixtureTransport(func(request *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	engine := sourceFixtureEngine(t, transport)
	engine.downloader.cfg.MaxPagesPerSort = 1
	if _, err := engine.startSourceTask(sourceHongguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("auto did not start")
	}
	engine.cancelSourceTask(sourceHongguo)
	status := awaitSourceTask(t, engine, sourceHongguo)
	if status.Stage != "已停止" || !status.RetryAt.IsZero() {
		t.Fatal(status)
	}
	restored := reopenCatalogEngine(t, engine.directory, transport)
	status, err := restored.startSourceTask(sourceHongguo, "auto", nativeDrama{})
	if err != nil || status.Running {
		t.Fatal("explicit stop lost on restart", status, err)
	}
	status, err = restored.startSourceTask(sourceHongguo, "update", nativeDrama{})
	if err != nil || !status.Running {
		t.Fatal("manual update did not resume", status, err)
	}
	restored.cancelSourceTask(sourceHongguo)
	awaitSourceTask(t, restored, sourceHongguo)
}

func TestNativeHongguoAutoResumesFailedBatchAfterRestart(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var resumed atomic.Bool
	transport := sourceFixtureTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/category/real-drama" {
			return nil, context.DeadlineExceeded
		}
		payload := nativeHongguoRequestForTest(t, request)
		if payload.Offset > 0 && fail.Load() {
			return nil, context.DeadlineExceeded
		}
		if !fail.Load() && payload.Offset > 0 {
			resumed.Store(true)
		}
		base := map[string]int{"default": 1000, "comic_series": 2000, "ai_series": 3000}[payload.Scene]
		return nativeHongguoResponse(request, payload.Offset+18, payload.Offset < 18, "fixture-"+payload.Scene, strconv.Itoa(base+payload.Offset)), nil
	})
	engine := sourceFixtureEngine(t, transport)
	engine.downloader.cfg.MaxPagesPerSort = 1
	if _, err := engine.startSourceTask(sourceHongguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	status := awaitSourceTask(t, engine, sourceHongguo)
	if status.Count != 3 || !status.HasMore || status.Error == "" || !time.Now().Before(status.RetryAt) {
		t.Fatal("failure lost progress or retry delay", status)
	}
	restored := reopenCatalogEngine(t, engine.directory, transport)
	if status, err := restored.startSourceTask(sourceHongguo, "auto", nativeDrama{}); err != nil || status.Running {
		t.Fatal("restart ignored retry delay", status, err)
	}
	fail.Store(false)
	restored.changeSourceRecord(sourceHongguo, func(record *nativeSourceRecord) { record.RetryAt = time.Time{} })
	if _, err := restored.startSourceTask(sourceHongguo, "auto", nativeDrama{}); err != nil {
		t.Fatal(err)
	}
	status = awaitSourceTask(t, restored, sourceHongguo)
	if status.Count != 6 || status.HasMore || status.Error != "" || !resumed.Load() {
		t.Fatal("failed to resume saved position", status)
	}
}
