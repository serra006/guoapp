package core

import (
	"fmt"
	"strconv"
	"strings"
)

func huangdouVIPFlag(item map[string]any) *bool {
	var known *bool
	for _, key := range []string{"is_vip", "isVip"} {
		value := strings.ToLower(mapString(item, key))
		if value == "true" || value == "1" || value == "y" {
			result := true
			return &result
		}
		if value == "false" || value == "0" || value == "n" {
			result := false
			known = &result
		}
	}
	switch strings.ToLower(mapString(item, "pay_type")) {
	case "vip":
		result := true
		return &result
	case "free":
		result := false
		known = &result
	}
	for _, episode := range huangdouVIPEpisodes(item) {
		if episode > 0 {
			result := true
			return &result
		}
	}
	for _, episode := range huangdouList(item["episodes"]) {
		if strings.EqualFold(mapString(episode, "type"), "vip") {
			result := true
			return &result
		}
	}
	return known
}

func huangdouVIPEpisodes(item map[string]any) []int {
	var result []int
	if values, ok := item["vip_episodes"].([]any); ok {
		for _, value := range values {
			if index, err := strconv.Atoi(fmt.Sprint(value)); err == nil && index > 0 && index <= 10000 {
				result = append(result, index)
			}
		}
	}
	return result
}

func huangdouEpisodeVIP(detail, episode map[string]any, seq int) bool {
	switch strings.ToLower(mapString(episode, "type")) {
	case "vip", "coin", "points", "money", "gold":
		return true
	case "free":
		return false
	}
	for _, index := range huangdouVIPEpisodes(detail) {
		if index == seq {
			return true
		}
	}
	// 付费分集不一定写进 vip_episodes：金币锁在 coin_episodes、积分锁在
	// points_episodes 两个独立数组里。少读这两个字段的后果是整季都显示成「可播」，
	// 用户点到第 2 集才拿到 813005「本集需金币解锁」，观感就是「只能播 1 集」。
	for _, key := range []string{"coin_episodes", "points_episodes"} {
		for _, index := range huangdouEpisodeIndexes(detail[key]) {
			if index == seq {
				return true
			}
		}
	}
	if flag := huangdouVIPFlag(episode); flag != nil {
		return *flag
	}
	switch strings.ToLower(mapString(detail, "pay_type")) {
	case "vip", "money", "coin", "points":
		// 整剧付费：upstream 的 free_episodes 是免费集数上限，超出的都要钱。
		if free, err := strconv.Atoi(mapString(detail, "free_episodes")); err == nil && free >= 0 {
			return seq > free
		}
	}
	return false
}

// huangdouEpisodeIndexes 把上游的 [2,3,4] 这类数组统一解析成集数。
func huangdouEpisodeIndexes(value any) []int {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]int, 0, len(values))
	for _, item := range values {
		if index, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(item))); err == nil && index > 0 && index <= 10000 {
			result = append(result, index)
		}
	}
	return result
}
