package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (downloader *Downloader) resolveHongguoAppMedia(ctx context.Context, videoID string) (providerMedia, error) {
	if !hongguoNumericID.MatchString(videoID) {
		return providerMedia{}, errors.New("红果视频 ID 无效")
	}
	var firstErr error

	// 取流入口零（2026-11 起为唯一稳定可用）：multi_video_model/v1/，用官方设备登记
	// 接口下发的 device_id/iid，且**不带三签名**。`video_platform: 1024` 与
	// `mixed_video_id_map` 的键 "1" 是入口要求（不是画质档位），照抄实测配方。
	// 响应形态：data[vid].video_model。
	multi := map[string]any{
		"mixed_video_id_map": map[string]any{"1": []string{videoID}},
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 3, "disable_digg_stat": false,
			"disable_video_relate_book": false, "need_all_video_definition": true,
			"need_mp4_align": false, "use_os_player": false, "use_server_dns": false,
			"video_platform": 1024,
		},
	}
	deviceID, installID := downloader.hongguoPlaybackIdentityOrFallback(ctx, false)
	result, err := downloader.hongguoAppPlaybackRequest(ctx, hongguoAppMultiVideoModelPath, deviceID, installID, multi)
	var identityErr error
	if err == nil {
		if media, mediaErr := hongguoAppMediaFromBatch(result, videoID); mediaErr == nil {
			return media, nil
		} else if firstErr == nil {
			firstErr, identityErr = mediaErr, mediaErr
		}
	} else if firstErr == nil {
		firstErr, identityErr = err, err
	}
	// 空响应 = 服务端不认这套身份。重新登记一次拿新身份，再试**一次**（不循环）。
	if hongguoPlaybackNeedsNewIdentity(identityErr) && ctx.Err() == nil {
		if retryDevice, retryInstall, registerErr := downloader.hongguoPlaybackIdentity(ctx, true); registerErr == nil {
			if result, err := downloader.hongguoAppPlaybackRequest(ctx, hongguoAppMultiVideoModelPath, retryDevice, retryInstall, multi); err == nil {
				if media, mediaErr := hongguoAppMediaFromBatch(result, videoID); mediaErr == nil {
					return media, nil
				}
			}
		}
	}

	// 取流入口一（既有回退，保留）：multi_video_model/preload/v1/（批量，签名链路）。
	// 红果会来回开合接口，preload 是上一代社区实测入口，任一恢复即自动接上。
	// 硬性要求：biz_param.video_platform=1024，mixed_video_id_map 的键 1004 = 短剧。
	preload := map[string]any{
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 3, "disable_digg_stat": false,
			"need_all_video_definition": true, "need_mp4_align": false,
			"use_os_player": false, "use_server_dns": false, "video_platform": 1024,
		},
		"mixed_video_id_map": map[string]any{"1004": []string{videoID}},
	}
	if result, err := downloader.hongguoAppRequest(ctx, http.MethodPost, "/novel/player/multi_video_model/preload/v1/", nil, preload); err == nil {
		if media, mediaErr := hongguoAppMediaFromBatch(result, videoID); mediaErr == nil {
			return media, nil
		} else if firstErr == nil {
			firstErr = mediaErr
		}
	} else if firstErr == nil {
		firstErr = err
	}
	// 取流入口二（既有回退，保留）：旧的单集入口。红果会来回开合接口——video_detail
	// 当天就随完整签名从停供变为可用，media 类入口也可能单独重开。
	legacy := map[string]any{
		"video_id": videoID, "content_type": 1,
		"biz_param": map[string]any{"need_all_video_definition": true, "video_platform": 3},
	}
	if result, err := downloader.hongguoAppRequest(ctx, http.MethodPost, "/novel/player/video_model/v1/", nil, legacy); err == nil {
		if media, mediaErr := hongguoAppMediaFromResult(result); mediaErr == nil {
			return media, nil
		} else if firstErr == nil {
			firstErr = mediaErr
		}
	} else if firstErr == nil {
		firstErr = err
	}
	if firstErr == nil {
		firstErr = errHongguoAppNoMedia
	}
	return providerMedia{}, firstErr
}

// hongguoPlaybackNeedsNewIdentity 判断失败是否属于「身份不被服务端接受」：
// HTTP 200 + 0 字节，或响应里根本没有本次请求的 vid。只有这两类才值得换身份重试。
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
