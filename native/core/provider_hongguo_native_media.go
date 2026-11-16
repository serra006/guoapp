package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (downloader *Downloader) resolveHongguoAppMedia(ctx context.Context, videoID string) (providerMedia, error) {
	if !hongguoNumericID.MatchString(videoID) {
		return providerMedia{}, errors.New("红果视频 ID 无效")
	}

	// 三条入口共用一套「登记身份 + 登记机型 + 先签后退不签」的发送方式
	// （见 hongguoPlaybackAttempt）。区别在集群、路径与 payload 形状：
	//   一、a 集群 video_model/v1/          —— 官方现行单集入口，响应 data.video_model
	//   二、b 集群 multi_video_model/v1/    —— 键 "1"（入口要求，不是画质档位）
	//   三、b 集群 multi_video_model/preload/v1/ —— 键 1004 = 短剧，上一代社区实测入口
	// 红果会把这几条入口来回开合，任一条恢复就自动接上。
	// 注意一、二两条的集群不同：同一个路径在另一个集群上不通用。
	single := map[string]any{
		"video_id": videoID,
		"biz_param": map[string]any{
			"video_id_type": 0, "need_all_video_definition": true, "video_platform": 3,
		},
	}
	multi := map[string]any{
		"mixed_video_id_map": map[string]any{"1": []string{videoID}},
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 3, "disable_digg_stat": false,
			"disable_video_relate_book": false, "need_all_video_definition": true,
			"need_mp4_align": false, "use_os_player": false, "use_server_dns": false,
			"video_platform": 1024,
		},
	}
	preload := map[string]any{
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 3, "disable_digg_stat": false,
			"need_all_video_definition": true, "need_mp4_align": false,
			"use_os_player": false, "use_server_dns": false, "video_platform": 1024,
		},
		"mixed_video_id_map": map[string]any{"1004": []string{videoID}},
	}
	client := downloader.hongguoClient()
	client.mu.Lock()
	appBase := client.baseURL
	client.mu.Unlock()
	batch := func(result map[string]any) (providerMedia, error) {
		return hongguoAppMediaFromBatch(result, videoID)
	}
	entries := []struct {
		base    string
		path    string
		payload map[string]any
		parse   func(map[string]any) (providerMedia, error)
	}{
		{hongguoPlaybackBaseURL, hongguoPlaybackVideoModelPath, single, hongguoAppMediaFromResult},
		{appBase, hongguoAppMultiVideoModelPath, multi, batch},
		{appBase, "/novel/player/multi_video_model/preload/v1/", preload, batch},
	}

	// 空体（HTTP 200 + 0 字节）是**服务端层面**的拒绝：既然一条入口已经空体，
	// 另外两条（还都是已停服的旧入口）大概率同样空，继续试只是让用户白等。
	// NoMedia 语义不同（响应正常、只是没有本次 vid），那种要继续试下一条。
	var firstErr error
	runAll := func(state hongguoIdentityState) (providerMedia, error) {
		for _, entry := range entries {
			media, err := downloader.hongguoPlaybackAttempt(ctx, entry.base, entry.path, state, entry.payload, entry.parse)
			if err == nil {
				return media, nil
			}
			if firstErr == nil {
				firstErr = err
			}
			if errors.Is(err, errHongguoAppEmpty) {
				break
			}
		}
		return providerMedia{}, firstErr
	}

	state := downloader.hongguoPlaybackIdentityOrFallback(ctx, false)

	// 零、现行 v5 入口（2026-11-16 起唯一还在应答的取址链路）。
	// 它自己带 gzip + 三元组签名，与下面三条老入口的发送路径完全不同，
	// 所以单独走一条分支，而不是塞进 entries 表里。
	if media, err := downloader.hongguoV5Attempt(ctx, state, videoID); err == nil {
		return media, nil
	} else if firstErr == nil {
		firstErr = err
	}

	if media, err := runAll(state); err == nil {
		return media, nil
	}
	// 只有「响应正常但没有本次 vid」才值得重新登记一次拿新身份再跑一遍（不是循环）。
	// 空体不重登记：6 台不同机型 × 3 个登记集群 × 游客票据齐全的实测结论是换身份也
	// 不会让它返回数据，重登记只会白送服务端一批无意义的设备登记记录。
	if errors.Is(firstErr, errHongguoAppNoMedia) && ctx.Err() == nil {
		if retryState, registerErr := downloader.hongguoPlaybackIdentityState(ctx, true); registerErr == nil {
			if media, err := runAll(retryState); err == nil {
				return media, nil
			}
		}
	}
	if firstErr == nil {
		firstErr = errHongguoAppNoMedia
	}
	return providerMedia{}, firstErr
}

// hongguoPlaybackAttempt 对单个入口做「先带签名、空响应再退不签名」的两次尝试。
//
// 红果在播放接口上对签名的要求历史上开合过（2026-11-01 要无签名，2026-11-03 又要
// 签名），把任何一轮结论写死都会在下一次开合时整体失效。签名只对「空响应 / 没有本次
// vid」这类软拒绝退让；HTTP 状态码异常、JSON 坏掉再试一次也没意义，直接上报。
func (downloader *Downloader) hongguoPlaybackAttempt(ctx context.Context, base, path string, state hongguoIdentityState,
	payload any, parse func(map[string]any) (providerMedia, error)) (providerMedia, error) {
	signed, signedErr := downloader.hongguoAppPlaybackRequest(ctx, base, path, state, payload, true)
	if signedErr == nil {
		if media, mediaErr := parse(signed); mediaErr == nil {
			return media, nil
		} else {
			signedErr = mediaErr
		}
	}
	if !hongguoPlaybackNeedsNewIdentity(signedErr) || ctx.Err() != nil {
		return providerMedia{}, signedErr
	}
	plain, plainErr := downloader.hongguoAppPlaybackRequest(ctx, base, path, state, payload, false)
	if plainErr != nil {
		return providerMedia{}, signedErr
	}
	if media, mediaErr := parse(plain); mediaErr == nil {
		return media, nil
	}
	return providerMedia{}, signedErr
}

// hongguoPlaybackNeedsNewIdentity 判断失败是否属于「凭证不被服务端接受」的软拒绝：
// HTTP 200 + 0 字节，或响应里根本没有本次请求的 vid。只有这两类才值得换一种凭证形态
// 再发一次（hongguoPlaybackAttempt 用它决定「签名被拒后是否退不签名」）。
// 注意：resolveHongguoAppMedia 只在 NoMedia 时重新登记身份，空体不重登记。
func hongguoPlaybackNeedsNewIdentity(err error) bool {
	return err != nil && (errors.Is(err, errHongguoAppEmpty) || errors.Is(err, errHongguoAppNoMedia))
}

// hongguoAppMediaFromResult 解析旧单集入口的响应：data.video_model 可能是对象，
// 也可能是一条 JSON 字符串。
func hongguoAppMediaFromResult(result map[string]any) (providerMedia, error) {
	data := nestedMap(result, "data")
	model, _ := data["video_model"].(map[string]any)
	if encoded, ok := data["video_model"].(string); ok {
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&model); err != nil {
			return providerMedia{}, errors.New("红果 App 播放信息格式异常")
		}
	}
	if len(model) == 0 {
		return providerMedia{}, errHongguoAppNoMedia
	}
	return selectHongguoAppMedia(model)
}

// hongguoAppMediaFromBatch 从批量取流响应里挑出本次请求的那个 vid 的 video_model。
// multi_video_model/v1/ 与 preload/v1/ 的响应形态一致（data[vid].video_model）。
// 只认请求的 vid，不退回「data 里唯一的那一项」——批量响应里那一项可能是别的集，
// 张冠李戴比报错糟得多（同详情腿核对 series_id 的口径）。
func hongguoAppMediaFromBatch(result map[string]any, videoID string) (providerMedia, error) {
	entry, _ := nestedMap(result, "data")[videoID].(map[string]any)
	model, _ := entry["video_model"].(map[string]any)
	if encoded, ok := entry["video_model"].(string); ok {
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&model); err != nil {
			return providerMedia{}, errors.New("红果 App 播放信息格式异常")
		}
	}
	if len(model) == 0 {
		return providerMedia{}, errHongguoAppNoMedia
	}
	return selectHongguoAppMedia(model)
}

func selectHongguoAppMedia(model map[string]any) (providerMedia, error) {
	variants := anyList(model["video_list"])
	if rows, ok := model["video_list"].(map[string]any); ok && len(variants) == 0 {
		keys := make([]string, 0, len(rows))
		for key := range rows {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			variants = append(variants, rows[key])
		}
	}
	duration, _ := strconv.ParseFloat(mapString(model, "video_duration", "duration"), 64)
	var keyErr error
	type scoredMedia struct {
		media providerMedia
		score int
	}
	var choices []scoredMedia
	for _, row := range variants {
		variant, _ := row.(map[string]any)
		meta := nestedMap(variant, "video_meta")
		codec := strings.ToLower(mapString(meta, "codec_type"))
		if codec == "bytevc2" || strings.Contains(strings.ToLower(mapString(variant, "gear_des_key")), "bytevc2") {
			continue
		}
		addresses := hongguoMediaAddresses(variant)
		if len(addresses) == 0 {
			continue
		}
		media := providerMedia{Referer: "https://novel.snssdk.com/", Duration: time.Duration(duration * float64(time.Second))}
		encryption := nestedMap(variant, "encrypt_info")
		spade := mapString(encryption, "spade_a")
		if spade != "" || encryption["encrypt"] == true || mapString(encryption, "encryption_method") == "cenc-aes-ctr" {
			var err error
			media.CENCKey, err = hongguoContentKey(spade)
			if err != nil {
				keyErr = err
				continue
			}
		}
		height, _ := strconv.Atoi(mapString(meta, "vheight"))
		if definition, err := strconv.Atoi(hongguoQualityNumber.FindString(mapString(meta, "definition"))); err == nil && definition > 0 {
			height = definition
		} else if width, _ := strconv.Atoi(mapString(meta, "vwidth")); width > 0 && (height == 0 || width < height) {
			height = width
		}
		media.Quality = height
		quality := height * 10
		if codec == "h264" || codec == "avc1" {
			quality++
		}
		for _, address := range addresses {
			media.URL = address
			choices = append(choices, scoredMedia{media: media, score: quality})
		}
	}
	if len(choices) > 0 {
		sort.SliceStable(choices, func(i, j int) bool { return choices[i].score > choices[j].score })
		selected := choices[0].media
		for _, choice := range choices {
			selected.Variants = append(selected.Variants, choice.media)
		}
		return selected, nil
	}
	if keyErr != nil {
		return providerMedia{}, fmt.Errorf("红果 App 媒体密钥不可用: %w", keyErr)
	}
	return providerMedia{}, errors.New("红果 App 未返回兼容的媒体，已跳过不支持的编码")
}

func hongguoMediaAddresses(info map[string]any) []string {
	var addresses []string
	seen := map[string]bool{}
	var add func(any)
	add = func(value any) {
		switch value := value.(type) {
		case string:
			address := strings.TrimSpace(value)
			if len(address) > 8192 {
				return
			}
			if !isProviderHTTPMediaURL(address) {
				decoded, err := decodeHongguoBase64(address)
				if err != nil {
					return
				}
				address = strings.TrimSpace(string(decoded))
			}
			if isProviderHTTPMediaURL(address) && !seen[address] {
				seen[address] = true
				addresses = append(addresses, address)
			}
		case []any:
			for _, item := range value {
				add(item)
			}
		}
	}
	for _, key := range []string{"main_url", "backup_url", "backup_url_1", "backup_url_2", "backup_urls", "url_list"} {
		add(info[key])
	}
	return addresses
}
