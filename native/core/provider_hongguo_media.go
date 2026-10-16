package core

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 红果多清晰度取流：multi_video_model 取模型 → fallback_api 取清晰度列表 →
// spade URL 解密 → spade_a 派生 CENC 内容密钥。签名头与参数顺序均需与客户端一致。

const (
	hongguoMultiVideoPath = "/novel/player/multi_video_model/v1/"
	hongguoMediaUserAgent = "com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"
	hongguoMediaVersion   = "7.1.3.32"
)

var hongguoSpadeConstants = func() []byte {
	decoded, err := hex.DecodeString("4dd4c2e6b83162090e52b3c7a6733ba41cb2462b829ab58a196b39db57177524f49baf7f08e8d68d26a72e37c1a95a2f1f05a51892aef2949732b62a38aadd58")
	if err != nil {
		panic("红果 spade 常量无效: " + err.Error())
	}
	return decoded
}()

var hongguoMediaQualityPattern = regexp.MustCompile(`(2160|1440|1080|720|576|540|480|360)`)

type hongguoQueryPair struct {
	key   string
	value string
}

func hongguoQueryEscape(value string) string {
	escaped := url.QueryEscape(value)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	return strings.ReplaceAll(escaped, "%2A", "*")
}

func hongguoMediaQuery(deviceID, installID string, khronos uint32, ticket uint64) string {
	pairs := []hongguoQueryPair{
		{"iid", installID},
		{"device_id", deviceID},
		{"ac", "wifi"},
		{"channel", "update_64"},
		{"aid", "8662"},
		{"app_name", "novelread"},
		{"version_code", "71332"},
		{"version_name", hongguoMediaVersion},
		{"device_platform", "android"},
		{"os", "android"},
		{"ssmix", "a"},
		{"device_type", "25053RT47C"},
		{"device_brand", "Redmi"},
		{"language", "zh"},
		{"os_api", "36"},
		{"os_version", "16"},
		{"manifest_version_code", "71332"},
		{"resolution", "1280*2772"},
		{"dpi", "520"},
		{"update_version_code", "71332"},
		{"host_abi", "arm64-v8a"},
		{"dragon_device_type", "phone"},
		{"pv_player", "71332"},
		{"compliance_status", "0"},
		{"need_personal_recommend", "1"},
		{"player_so_load", "1"},
		{"is_android_pad_screen", "0"},
		{"ts", strconv.FormatUint(uint64(khronos), 10)},
		{"_rticket", strconv.FormatUint(ticket, 10)},
	}
	var builder strings.Builder
	for index, pair := range pairs {
		if index > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString(hongguoQueryEscape(pair.key))
		builder.WriteByte('=')
		builder.WriteString(hongguoQueryEscape(pair.value))
	}
	return builder.String()
}

type hongguoMediaCall struct {
	url     string
	body    []byte
	headers map[string]string
}

func hongguoMediaPayload(videoID string) []byte {
	return []byte(`{"biz_param":{"detail_page_version":0,"device_level":3,"disable_digg_stat":false,"need_all_video_definition":true,"need_mp4_align":false,"use_os_player":false,"use_server_dns":false,"video_platform":1024},"mixed_video_id_map":{"1004":["` + videoID + `"]}}`)
}

func hongguoBuildMediaCall(videoID, deviceID, installID string) hongguoMediaCall {
	body := hongguoMediaPayload(videoID)
	khronos := uint32(time.Now().Unix())
	baseTicket := uint64(time.Now().UnixMilli())
	var rawURL, query string
	var requestTicket uint64
	// branch 1 的签名变体服务端不认，抖动 _rticket 直到落在其它分支。
	for offset := uint64(0); offset < 32; offset++ {
		requestTicket = baseTicket + offset
		query = hongguoMediaQuery(deviceID, installID, khronos, requestTicket)
		rawURL = hongguoAppBaseURL + hongguoMultiVideoPath + "?" + query
		if hongguoBranchOf(rawURL, body, khronos) != 1 {
			break
		}
	}
	random := hongguoRandom32() % 65536
	bodyHash := md5.Sum(body)
	stamp := make([]byte, 4)
	binary.BigEndian.PutUint32(stamp, khronos)
	little := make([]byte, 4)
	binary.LittleEndian.PutUint32(little, khronos)
	headers := map[string]string{
		"User-Agent":                hongguoMediaUserAgent,
		"Accept":                    "application/json; charset=utf-8,application/x-protobuf",
		"Content-Type":              "application/json; charset=UTF-8",
		"X-XS-From-Web":             "0",
		"X-SS-Req-Ticket":           strconv.FormatUint(requestTicket, 10),
		"X-TT-Request-Tag":          "t=0;n=0",
		"Sdk-Version":               "2",
		"Passport-Sdk-Version":      "50561",
		"X-VC-BDTuring-SDK-Version": "3.7.2.cn",
		"X-SS-STUB":                 strings.ToUpper(hex.EncodeToString(bodyHash[:])),
		"X-Gorgon":                  hongguoXGorgon(query, body, khronos, random),
		"X-Khronos":                 strconv.FormatUint(uint64(khronos), 10),
		"X-Ladon":                   base64.StdEncoding.EncodeToString(stamp),
		"X-Argus":                   base64.StdEncoding.EncodeToString(little),
		"X-Helios":                  hongguoHelios(khronos),
		"X-Medusa":                  hongguoBuildMedusa(rawURL, body, khronos, deviceID, hongguoMediaVersion),
		"X-TT-DT":                   "",
	}
	return hongguoMediaCall{url: rawURL, body: body, headers: headers}
}

func (d *Downloader) hongguoMediaModel(ctx context.Context, videoID, deviceID, installID string) (map[string]any, error) {
	call := hongguoBuildMediaCall(videoID, deviceID, installID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, call.url, bytes.NewReader(call.body))
	if err != nil {
		return nil, err
	}
	for name, value := range call.headers {
		request.Header.Set(name, value)
	}
	response, err := d.doPreparedCatalogRequest(request, 20*time.Second, nil)
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
	response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("红果取流接口 HTTP %d", response.StatusCode)
	}
	if len(content) == 0 || len(content) > providerMaxBodyBytes {
		return nil, errors.New("红果取流接口未返回有效数据")
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("红果取流接口返回格式异常")
	}
	code := firstNonEmpty(mapString(result, "code", "Code", "status_code"), mapString(nestedMap(result, "BaseResp"), "StatusCode"))
	if code != "" && code != "0" {
		return nil, fmt.Errorf("红果取流接口暂不可用（%s）", truncate(code, 20))
	}
	return result, nil
}

func hongguoMediaModelEntry(result map[string]any, videoID string) (map[string]any, error) {
	data := nestedMap(result, "data")
	if data == nil {
		return nil, errors.New("红果取流接口没有返回数据")
	}
	entry, _ := data[videoID].(map[string]any)
	if entry == nil {
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if candidate, ok := data[key].(map[string]any); ok {
				entry = candidate
				break
			}
		}
	}
	if entry == nil {
		return nil, errors.New("红果取流接口没有该视频的播放信息")
	}
	if encoded, ok := entry["video_model"].(string); ok {
		var model map[string]any
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&model); err != nil {
			return nil, errors.New("红果播放信息格式异常")
		}
		return model, nil
	}
	model, _ := entry["video_model"].(map[string]any)
	if model == nil {
		return nil, errors.New("红果播放信息内容为空")
	}
	return model, nil
}

func hongguoFallbackAddress(model map[string]any) string {
	value := model["fallback_api"]
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.HasPrefix(trimmed, "{") {
			var parsed map[string]any
			if json.Unmarshal([]byte(trimmed), &parsed) == nil {
				if nested := mapString(parsed, "fallback_api"); nested != "" {
					return nested
				}
			}
		}
		return trimmed
	case []any:
		if len(typed) > 0 {
			return nativeText(typed[0])
		}
	case map[string]any:
		return mapString(typed, "fallback_api")
	}
	return ""
}

func (d *Downloader) hongguoFallbackInfo(ctx context.Context, address string) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", hongguoMediaUserAgent)
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	response, err := d.doPreparedCatalogRequest(request, 20*time.Second, nil)
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
	response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("红果清晰度接口 HTTP %d", response.StatusCode)
	}
	var outer map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&outer); err != nil {
		return nil, errors.New("红果清晰度接口返回格式异常")
	}
	return nestedMap(nestedMap(outer, "video_info"), "data"), nil
}

func hongguoSpadeAddress(value string, seed []byte) (string, error) {
	encoded := strings.TrimSpace(value)
	if encoded == "" {
		return "", nil
	}
	raw, err := hongguoDecodeBase64URLSafe(encoded)
	if err != nil || len(raw) < 5 {
		return "", errors.New("红果 spade 地址密文过短")
	}
	if raw[0] != 0xa8 || raw[2] != 0x01 || raw[3] != 0x00 {
		return "", errors.New("红果 spade 地址头格式错误")
	}
	cipherLength := (len(raw) - 4) / 16 * 16
	if cipherLength == 0 {
		return "", errors.New("红果 spade 地址没有完整密文块")
	}
	first := sha512.Sum512(seed)
	second := sha512.Sum512(append(append([]byte{}, first[:]...), hongguoSpadeConstants...))
	block, err := aes.NewCipher(second[:16])
	if err != nil {
		return "", err
	}
	plaintext := make([]byte, cipherLength)
	cipher.NewCBCDecrypter(block, second[16:32]).CryptBlocks(plaintext, raw[4:4+cipherLength])
	if len(plaintext) > 0 {
		padding := int(plaintext[len(plaintext)-1])
		if padding >= 1 && padding <= 16 && padding <= len(plaintext) {
			plaintext = plaintext[:len(plaintext)-padding]
		}
	}
	for len(plaintext) > 0 && plaintext[len(plaintext)-1] == 0 {
		plaintext = plaintext[:len(plaintext)-1]
	}
	return string(plaintext), nil
}

func hongguoDecodeBase64URLSafe(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("-", "+", "_", "/").Replace(value)
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func hongguoMediaQualityNumber(value string) int {
	text := strings.TrimSpace(value)
	if matched := hongguoMediaQualityPattern.FindString(text); matched != "" {
		number, _ := strconv.Atoi(matched)
		return number
	}
	switch text {
	case "1920":
		return 1080
	case "1280":
		return 720
	case "1024":
		return 576
	case "854":
		return 480
	case "640":
		return 360
	}
	if len(text) >= 3 && len(text) <= 4 {
		if number, err := strconv.Atoi(text); err == nil {
			return number
		}
	}
	return 0
}

func hongguoQualityLabel(value string) string {
	if number := hongguoMediaQualityNumber(value); number > 0 {
		return strconv.Itoa(number) + "P"
	}
	labels := map[string]string{
		"low": "流畅", "smooth": "流畅", "medium": "标清", "normal": "高清",
		"high": "超清", "original": "原画", "uhd": "原画", "super_high": "原画",
	}
	if label, found := labels[strings.ToLower(strings.TrimSpace(value))]; found {
		return label
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "自动"
	}
	return trimmed
}

type hongguoQualityRow struct {
	key     string
	quality string
	item    map[string]any
}

func hongguoQualityRows(videoList map[string]any) []hongguoQualityRow {
	var rows []hongguoQualityRow
	for key, value := range videoList {
		item, _ := value.(map[string]any)
		if item == nil {
			item = map[string]any{}
		}
		if mapString(item, "main_url") == "" {
			continue
		}
		duplicated := false
		for _, row := range rows {
			if row.key == key {
				duplicated = true
				break
			}
		}
		if duplicated {
			continue
		}
		quality := firstNonEmpty(mapString(item, "quality_desc", "height", "vheight", "quality"), key)
		rows = append(rows, hongguoQualityRow{key: key, quality: quality, item: item})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left := firstNonEmpty(rows[i].quality, rows[i].key)
		right := firstNonEmpty(rows[j].quality, rows[j].key)
		return hongguoMediaQualityNumber(left) > hongguoMediaQualityNumber(right)
	})
	return rows
}

func (downloader *Downloader) resolveHongguoMultiMedia(ctx context.Context, videoID string) (providerMedia, error) {
	client := downloader.hongguoClient()
	client.mu.Lock()
	deviceID, installID := client.state.DeviceID, client.state.InstallID
	client.mu.Unlock()
	result, err := downloader.hongguoMediaModel(ctx, videoID, deviceID, installID)
	if err != nil {
		return providerMedia{}, err
	}
	model, err := hongguoMediaModelEntry(result, videoID)
	if err != nil {
		return providerMedia{}, err
	}
	address := hongguoFallbackAddress(model)
	if address == "" {
		return providerMedia{}, errors.New("红果未提供清晰度接口地址")
	}
	info, err := downloader.hongguoFallbackInfo(ctx, address)
	if err != nil {
		return providerMedia{}, err
	}
	videoList, _ := info["video_list"].(map[string]any)
	if len(videoList) == 0 {
		return providerMedia{}, errors.New("红果未返回可用的清晰度列表")
	}
	seed, _ := hongguoDecodeBase64URLSafe(mapString(info, "key_seed"))
	rows := hongguoQualityRows(videoList)
	if len(rows) == 0 {
		return providerMedia{}, errors.New("红果清晰度列表没有可播放地址")
	}
	duration, _ := strconv.ParseFloat(mapString(model, "video_duration", "duration"), 64)
	var keyErr error
	type scoredMedia struct {
		media providerMedia
		rank  int
	}
	var choices []scoredMedia
	for _, row := range rows {
		address := mapString(row.item, "main_url")
		if len(seed) > 0 && address != "" {
			decrypted, decryptErr := hongguoSpadeAddress(address, seed)
			if decryptErr != nil {
				keyErr = decryptErr
				continue
			}
			address = decrypted
		}
		if !isProviderHTTPMediaURL(address) {
			continue
		}
		meta := nestedMap(row.item, "video_meta")
		codec := strings.ToLower(mapString(meta, "codec_type"))
		if codec == "bytevc2" {
			continue
		}
		media := providerMedia{
			URL:      address,
			Referer:  "https://novel.snssdk.com/",
			Duration: time.Duration(duration * float64(time.Second)),
		}
		if spade := mapString(row.item, "spade_a"); spade != "" {
			key, keyErrValue := hongguoContentKey(spade)
			if keyErrValue != nil {
				keyErr = keyErrValue
				continue
			}
			media.CENCKey = key
		}
		height := hongguoMediaQualityNumber(row.quality)
		media.Quality = height
		media.Codec = codec
		choices = append(choices, scoredMedia{media: media, rank: providerCodecRank(codec)})
	}
	if len(choices) == 0 {
		if keyErr != nil {
			return providerMedia{}, fmt.Errorf("红果媒体密钥不可用: %w", keyErr)
		}
		return providerMedia{}, errors.New("红果未返回兼容的媒体，已跳过不支持的编码")
	}
	sort.SliceStable(choices, func(i, j int) bool {
		if choices[i].rank != choices[j].rank {
			return choices[i].rank < choices[j].rank
		}
		return choices[i].media.Quality > choices[j].media.Quality
	})
	selected := choices[0].media
	for _, choice := range choices {
		selected.Variants = append(selected.Variants, choice.media)
	}
	return selected, nil
}

// 取流阶段顺带拉取 m3u8：播放时本机流服务直接返回缓存内容，省掉一次远程往返。
// 预取失败不阻断播放，交由播放阶段按既有重试处理。
func (downloader *Downloader) prepareHongguoPlaylist(ctx context.Context, media providerMedia) providerMedia {
	if media.Playlist != "" || !isProviderHTTPMediaURL(media.URL) {
		return media
	}
	parsed, err := url.Parse(media.URL)
	if err != nil || !strings.HasSuffix(strings.ToLower(parsed.Path), ".m3u8") {
		return media
	}
	playlist, finalURL, err := downloader.fetchMediaPlaylist(ctx, media.URL, media.Referer)
	if err != nil {
		return media
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(playlist, "\ufeff")), "#EXTM3U") {
		return media
	}
	media.Playlist = playlist
	media.URL = finalURL
	if duration := m3u8Duration(playlist); duration > 0 {
		media.Duration = duration
	}
	return media
}
