package core

// 红果 v5 取流的「完整设备信封」形态（2026-11-25 起线上在用）。
//
// 2026-11-24 凌晨把 medusa message field 21 从 377 改成 312 只能好一天；
// 11-24 下午起同样的请求又全部拿到 HTTP 200 + 0 字节。2026-11-25 晚上
// N3urda/hongguoTV 0.16.15 提供了可复现的 A/B：在 body、身份、时间、传输都不变的前提
// 下，只要把 medusa 的 device 信封从 14 个 tag 补齐到 36 个（至 tag 38），并换用
// 74532 / 7.4.5.32 的 profile，就能重新拿到 24KB 的 video_model；74332 profile
// 即使用同一份完整信封仍然是 0 字节。
//
// 我们本地 2026-11-26 06:10 用生产链路验证过：
//   player/v1   24595 字节 → 1080P / cenc 16B / 10 variants，Range 206 video/mp4
//   preload/v1  24404 字节 → 同上
//
// 来源：https://github.com/N3urda/hongguoTV/blob/69f219e/kotlin-tv/SIGNING_V5.md
// 与同 SHA 的 kotlin-tv/core/.../Signer.kt（GPL-3.0-only 派生，字段表与推导过程公开）。
// 这里只按字段表重新实现，没有复制 Kotlin 代码。

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"sync"
	"time"
)

// 空响应冷却：N3urda issue #116 的结论是「短时间打满 v5 会让上游对整台设备返回
// 200+0」。我们旧路径一次播放会连发多个入口，反而延长故障。空响应后 60 秒内
// 不再发 v5 请求，直接让上层走别的入口；收到任何有效模型立刻清零。
const hongguoV5EmptyCooldown = 60 * time.Second

var hongguoV5EmptyGate struct {
	mu    sync.Mutex
	until time.Time
}

// hongguoV5EnvelopeNowMillis 是信封 device field 28 的时钟源。生产走 wall clock；
// 单测替换成固定值，才能在固定随机源下逐字节比对两次签名（否则两次调用跨毫秒时
// field 28 不同，确定性断言会偶发失败）。
var hongguoV5EnvelopeNowMillis = func() int64 { return time.Now().UnixMilli() }

func hongguoV5EmptyCooling() bool {
	hongguoV5EmptyGate.mu.Lock()
	defer hongguoV5EmptyGate.mu.Unlock()
	return time.Now().Before(hongguoV5EmptyGate.until)
}

func hongguoV5MarkEmpty() {
	hongguoV5EmptyGate.mu.Lock()
	defer hongguoV5EmptyGate.mu.Unlock()
	hongguoV5EmptyGate.until = time.Now().Add(hongguoV5EmptyCooldown)
}

func hongguoV5ClearEmptyCooldown() {
	hongguoV5EmptyGate.mu.Lock()
	defer hongguoV5EmptyGate.mu.Unlock()
	hongguoV5EmptyGate.until = time.Time{}
}

// hongguoV5EnvelopeProfile 是完整信封形态使用的客户端档案：7.4.5.32。
// 74332 在 2026-11-25 之后已经确认拿不到播放模型，别改回去。
func hongguoV5EnvelopeProfile(state hongguoIdentityState) hongguoV5Profile {
	profile := hongguoV5DeviceFromState(state)
	profile.VersionCode = "74532"
	profile.VersionName = "7.4.5.32"
	return profile
}

func hongguoV5Fixed32(out []byte, tag int, value uint32) []byte {
	out = binary.AppendUvarint(out, uint64(tag)<<3|5)
	return binary.LittleEndian.AppendUint32(out, value)
}

// hongguoV5EnvelopeDeviceToken 是 medusa message f(16) 与 device f(5) 共用的令牌：
// 24 字符 base64url 进程 nonce 前加 'A' 凑成 25 字符。
func hongguoV5EnvelopeDeviceToken(nonce string) string {
	token := nonce
	if len(token) > 24 {
		token = token[:24]
	}
	for len(token) < 24 {
		token += "A"
	}
	return "A" + token
}

// hongguoV5EnvelopeMedusaWith 用完整 36 字段 device 信封生成 X-Medusa。
// next 是随机源；额外两次抽取用于 env 的启动序号与进程号。
func hongguoV5EnvelopeMedusaWith(profile hongguoV5Profile, query string, body []byte, stamp uint32,
	nonce string, started int64, next func() (uint32, error)) (string, error) {
	queryHash := hongguoSM3([]byte(query))
	bodyHash := md5.Sum(body)
	f13 := hongguoV5F13(queryHash[:], bodyHash[:], stamp)
	deviceToken := hongguoV5EnvelopeDeviceToken(nonce)

	envLaunchSeed, err := next()
	if err != nil {
		return "", err
	}
	envPidSeed, err := next()
	if err != nil {
		return "", err
	}
	envLaunch := 100 + int(envLaunchSeed%21)
	envPid := 10001 + int(envPidSeed%2000)
	brand := profile.Brand
	if brand == "" {
		brand = "HONOR"
	}
	nowMillis := hongguoV5EnvelopeNowMillis()

	deviceInfo := hongguoV5Proto(
		hongguoV5SF(1, 1), hongguoV5SF(2, 2), hongguoV5SF(3, "8662"),
		hongguoV5SF(4, profile.DeviceID), hongguoV5SF(5, deviceToken), hongguoV5SF(6, "!noperm!"),
		hongguoV5SF(7, -888888), hongguoV5SF(8, -888888), hongguoV5SF(9, 3), hongguoV5SF(10, -888888),
		hongguoV5SF(11, "!notset!"), hongguoV5SF(12, "Asia/Shanghai,8"), hongguoV5SF(13, "zh_CN"),
		hongguoV5SF(14, 4),
	)
	for tag := 16; tag <= 21; tag++ {
		deviceInfo = hongguoV5Fixed32(deviceInfo, tag, 0)
	}
	deviceInfo = append(deviceInfo, hongguoV5Proto(
		hongguoV5SF(22, profile.OSVersion), hongguoV5SF(23, 41), hongguoV5SF(24, 36),
		hongguoV5SF(25, started), hongguoV5SF(26, started), hongguoV5SF(27, started),
		hongguoV5SF(28, nowMillis), hongguoV5SF(29, -1),
		hongguoV5SF(30, profile.Model), hongguoV5SF(31, brand),
		hongguoV5SF(32, profile.Model), hongguoV5SF(33, profile.Model),
		hongguoV5SF(34, brand), hongguoV5SF(35, brand), hongguoV5SF(36, brand),
		hongguoV5SF(38, 31),
	)...)

	environment := hongguoV5Proto(
		hongguoV5SF(1, envLaunch), hongguoV5SF(2, 247235599), hongguoV5SF(3, 247235596),
		hongguoV5SF(5, 7), hongguoV5SF(6, hongguoV5EnvVersion), hongguoV5SF(7, envPid),
		hongguoV5SF(8, profile.Channel), hongguoV5SF(12, deviceInfo),
		hongguoV5SF(13, hongguoV5Proto(
			hongguoV5SF(1, int64(stamp)), hongguoV5SF(2, -2), hongguoV5SF(4, 200),
		)),
		hongguoV5SF(14, profile.VersionName),
	)

	flags := []byte{0x44, 0x80, 0, 0, 0, 0, 0, 0}
	counters := hongguoV5Proto(
		hongguoV5SF(1, 305), hongguoV5SF(2, 4), hongguoV5SF(3, 694367), hongguoV5SF(5, 1621072674),
	)
	messageRandom, err := next()
	if err != nil {
		return "", err
	}
	message := hongguoV5Proto(
		hongguoV5SF(1, hongguoV5Magic), hongguoV5SF(2, 5), hongguoV5SF(3, messageRandom),
		hongguoV5SF(4, hongguoPlaybackAID), hongguoV5SF(5, profile.DeviceID), hongguoV5SF(6, hongguoV5License),
		hongguoV5SF(7, profile.VersionName), hongguoV5SF(8, hongguoV5ProtoVersion),
		hongguoV5SF(9, int64(hongguoV5SDKVersionCode)), hongguoV5SF(10, flags),
		hongguoV5SF(12, stamp), hongguoV5SF(13, f13), hongguoV5SF(14, queryHash[:6]),
		hongguoV5SF(15, counters), hongguoV5SF(16, deviceToken), hongguoV5SF(17, stamp),
		hongguoV5SF(20, "none"), hongguoV5SF(21, hongguoV5MessageField21),
		hongguoV5SF(23, environment),
		hongguoV5SF(24, `{"cmr":16777216,"cmr2":16777216,"un_h":11,"vpn":0,"kd":0,"dyn":"","do":1,"tk":true}`),
	)

	random, err := next()
	if err != nil {
		return "", err
	}
	keyHash := hongguoSM3(append(append(append([]byte(nil), hongguoV5SignKey...), hongguoV5LE32(random)...), hongguoV5SignKey...))
	high := (random >> 16) & 255
	seed := hongguoV5LE32(^(((high << 11) | (random >> 24)) ^ (high >> 5) ^ high))
	transformed := append(append([]byte(nil), flags...), hongguoV5XMXor(message, keyHash[:])...)
	for left, right := 0, len(transformed)-1; left < right; left, right = left+1, right-1 {
		transformed[left], transformed[right] = transformed[right], transformed[left]
	}
	for index := range transformed {
		transformed[index] ^= seed[(^index)&3]
	}
	check := uint32(0x18000001) | uint32(queryHash[0]&63)<<14 | uint32(f13[0]&63)<<8
	nonceRandom, err := next()
	if err != nil {
		return "", err
	}
	packed := append([]byte{0xca}, hongguoV5LE32(nonceRandom)...)
	packed = append(packed, hongguoV5LE32(check)...)
	packed = append(packed, transformed...)
	packed = append(packed, byte(random>>16), byte(random>>24))

	aesKey := md5.Sum(hongguoV5SignKey[:16])
	aesIV := md5.Sum(hongguoV5SignKey[16:])
	encrypted := newHongguoV5AES(aesKey[:], stamp).encrypt(packed, aesIV[:])
	prefix := append(hongguoV5LE32(5), hongguoV5Magic...)
	stampBytes := hongguoV5LE32(stamp)
	for index := range prefix {
		prefix[index] ^= stampBytes[index&3]
	}
	envelope := append(prefix, byte(random), byte(random>>8), 0, 1)
	envelope = append(envelope, encrypted...)
	return base64.StdEncoding.EncodeToString(envelope), nil
}

func hongguoV5EnvelopeMedusa(profile hongguoV5Profile, query string, body []byte, stamp uint32) (string, error) {
	return hongguoV5EnvelopeMedusaWith(profile, query, body, stamp,
		hongguoV5ProcessNonce, hongguoV5ProcessStarted, hongguoV5Random)
}

// hongguoV5EnvelopePlaybackRequest 走完整信封形态的现行取流入口。
func (downloader *Downloader) hongguoV5EnvelopePlaybackRequest(ctx context.Context, state hongguoIdentityState, videoID string) (map[string]any, error) {
	return downloader.hongguoV5EnvelopePost(ctx, hongguoV5Path, hongguoV5Payload(videoID), state)
}

func (downloader *Downloader) hongguoV5EnvelopePost(ctx context.Context, path string, payload map[string]any, state hongguoIdentityState) (map[string]any, error) {
	profile := hongguoV5EnvelopeProfile(state)
	signer := func(pf hongguoV5Profile, body []byte, now time.Time) (string, http.Header, error) {
		query, err := hongguoV5TuneTicket(pf, body, now)
		if err != nil {
			return "", nil, err
		}
		header, err := hongguoV5HeaderFor(pf, query, body, uint32(now.Unix()), now.UnixMilli())
		if err != nil {
			return "", nil, err
		}
		medusa, err := hongguoV5EnvelopeMedusa(pf, query, body, uint32(now.Unix()))
		if err != nil {
			return "", nil, err
		}
		header.Set("X-Medusa", medusa)
		return query, header, nil
	}
	return downloader.hongguoV5Post(ctx, path, payload, profile, signer)
}
