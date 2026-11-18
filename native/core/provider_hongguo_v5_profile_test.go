package core

// v5 取流的请求形状回归（离线，不联网）。
//
// 2026-11-18 全线空体之后定位到的根因有两层：
//  1. 请求形状不对：query 只有 13 项（客户端发 29 项）、`_rticket` 没落在第 4 槽、
//     头缺匿名头组，UA 还是 7.3.5.32 时代的样子；
//  2. **分支门禁算错**：分支 nibble 要吃 sm3(query)/md5(body) 的摘要，我们喂的是
//     原始字节 → 只有 1/16 的概率与服务端撞上，其余 15/16 被静默拒绝。
//
// 这组断言把「29 项顺序 / _rticket 槽位 / 签名串与发送串同一份 / 分支入参口径 /
// 头清单 / UA 版本」全部钉住，免得下次又只改一处。

import (
	"crypto/md5"
	"strconv"
	"strings"
	"testing"
	"time"
)

// hongguoV5ExpectedKeys 是 7.4.3.32 客户端的 query 顺序（openhongguo v0.3.1
// protocol.PlaybackV5Pairs 的逐项拷贝）。顺序变了签名就变了，所以这里写死。
var hongguoV5ExpectedKeys = []string{
	"device_platform", "os", "ssmix", "_rticket", "cdid", "channel", "aid", "app_name",
	"version_code", "version_name", "manifest_version_code", "update_version_code",
	"resolution", "dpi", "device_type", "device_brand", "language", "os_api",
	"os_version", "ac", "host_abi", "dragon_device_type", "pv_player",
	"compliance_status", "need_personal_recommend", "player_so_load",
	"is_android_pad_screen", "iid", "device_id",
}

// hongguoV5TestState 是一套「登记机型与 v5 常量故意不同」的身份，用来证明
// 登记侧的机型/系统/渠道不会被带进 v5 请求。
func hongguoV5TestState() hongguoIdentityState {
	return hongguoIdentityState{
		DeviceID: "7702409114256690238", InstallID: "7702409114256690239", Registered: true,
		CDID: "6c1f3a0e2b7d4958a1c0e5f42d8b7a13", OpenUDID: "0f1e2d3c4b5a6978",
		TTReq: "ttreq-test-ticket", StoreRegion: "cn", StoreRegionSrc: "cn",
		DeviceModel: "22021211RC", DeviceBrand: "Xiaomi", DeviceManufacturer: "Xiaomi",
		DeviceOSVersion: "13", DeviceOSAPI: "33", DeviceBuild: "TKQ1.221114.001",
		DeviceResolution: "1080x2400", DeviceDPI: "440", DeviceChannel: "update_64",
	}
}

// hongguoV5UserAgentVersion 取 UA 里 "com.phoenix.read/<version>" 那一段。
func hongguoV5UserAgentVersion(t *testing.T, userAgent string) string {
	t.Helper()
	const prefix = "com.phoenix.read/"
	if !strings.HasPrefix(userAgent, prefix) {
		t.Fatalf("UA 不是红果客户端形态: %q", userAgent)
	}
	rest := strings.TrimPrefix(userAgent, prefix)
	end := strings.IndexAny(rest, " (")
	if end <= 0 {
		t.Fatalf("UA 里没有版本号: %q", userAgent)
	}
	return rest[:end]
}

// hongguoV5QueryKeys 按出现顺序切出 query 的 key 列表。
func hongguoV5QueryKeys(query string) []string {
	fields := strings.Split(query, "&")
	keys := make([]string, 0, len(fields))
	for _, field := range fields {
		key, _, _ := strings.Cut(field, "=")
		keys = append(keys, key)
	}
	return keys
}

// hongguoV5QueryValue 取 query 里某个 key 的原值（不做二次解码：
// 这组用例里的值都不含需要反转义的字符）。
func hongguoV5QueryValue(t *testing.T, query, key string) string {
	t.Helper()
	for _, field := range strings.Split(query, "&") {
		name, value, _ := strings.Cut(field, "=")
		if name == key {
			return value
		}
	}
	t.Fatalf("query 里缺少 %s: %q", key, query)
	return ""
}

func TestHongguoV5PlaybackProfilePinned(t *testing.T) {
	state := hongguoV5TestState()
	body, err := hongguoV5Gzip([]byte(`{"probe":1}`))
	if err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	profile := hongguoV5DeviceFromState(state)
	now := time.Unix(1764550000, 0)
	query, header, err := hongguoV5Headers(profile, body, now)
	if err != nil {
		t.Fatalf("生成 v5 请求头失败: %v", err)
	}

	// 1) UA：v5 原文（Cronet 形态 + 74332），不得混进登记侧的版本与机型。
	userAgent := header.Get("User-Agent")
	if want := hongguoV5UserAgent(); userAgent != want {
		t.Fatalf("UA 不是 v5 原文:\n got %q\nwant %q", userAgent, want)
	}
	if !strings.HasPrefix(userAgent, "com.phoenix.read/74332") {
		t.Fatalf("UA 版本前缀不是 /74332: %q", userAgent)
	}
	if !strings.Contains(userAgent, hongguoV5DeviceModel) {
		t.Fatalf("UA 里没有固定机型 %s: %q", hongguoV5DeviceModel, userAgent)
	}
	// 上半轮那套 smovie 形态（73932 + tt-ok）与登记侧方言都不得留在这里。
	for _, banned := range []string{hongguoRegisterVersion, "73932", "tt-ok", "PGT-AN10", "vivo_8662_64",
		state.DeviceModel, state.DeviceBuild} {
		if strings.Contains(userAgent, banned) {
			t.Fatalf("UA 里混进了 %q: %q", banned, userAgent)
		}
	}
	if !strings.Contains(userAgent, "Cronet/TTNetVersion:8d40f833 2026-03-03 QuicVersion:21ac1950 2025-11-18") {
		t.Fatalf("UA 少了 Cronet 尾巴: %q", userAgent)
	}

	// 2) query 必须是 29 项且顺序与上游逐项一致。
	keys := hongguoV5QueryKeys(query)
	if len(keys) != len(hongguoV5ExpectedKeys) {
		t.Fatalf("query 有 %d 项，应为 %d 项: %q", len(keys), len(hongguoV5ExpectedKeys), query)
	}
	for index, want := range hongguoV5ExpectedKeys {
		if keys[index] != want {
			t.Fatalf("query 第 %d 项是 %q，应为 %q\n%s", index, keys[index], want, query)
		}
	}
	if keys[3] != "_rticket" {
		t.Fatalf("_rticket 不在第 4 槽（0 基索引 3）: %q", query)
	}
	if ticket := hongguoV5QueryValue(t, query, "_rticket"); ticket == "" {
		t.Fatal("_rticket 是空的")
	}
	// resolution 里的 `*` 属于 unreserved，必须原样发出去：转成 %2A 会让签名串与
	// 上游客户端不一致。
	if !strings.Contains(query, "resolution="+hongguoV5Resolution) {
		t.Fatalf("resolution 被转义了: %q", query)
	}
	if strings.Contains(query, "%2A") || strings.Contains(query, "%2a") {
		t.Fatalf("query 里出现了被转义的星号: %q", query)
	}

	// 3) 签名的串必须与发出的串逐字节相同：用 query 里的 ticket 重建一次，
	//    走同一条编码路径比对。任何一处编码差异（+ / %20 / 字母序）都会在这里炸。
	pairs := profile.pairs()
	rebuilt := make([][2]string, 0, len(pairs))
	for _, pair := range pairs {
		if pair[0] == "_rticket" {
			pair[1] = hongguoV5QueryValue(t, query, "_rticket")
		}
		rebuilt = append(rebuilt, pair)
	}
	if encoded := hongguoV5EncodePairs(rebuilt); encoded != query {
		t.Fatalf("签名串与实际发送串不一致:\n signed %q\n  sent %q", encoded, query)
	}

	// 4) query 的设备档案必须是固定常量（含 DefaultDevice 的分辨率/DPI/API）。
	for key, want := range map[string]string{
		"device_platform":         "android",
		"os":                      "android",
		"ssmix":                   "a",
		"aid":                     hongguoPlaybackAID,
		"app_name":                "novelread",
		"channel":                 hongguoV5Channel,
		"device_type":             hongguoV5DeviceModel,
		"device_brand":            hongguoV5DeviceBrand,
		"os_version":              hongguoV5OSVersion,
		"os_api":                  hongguoV5OSAPI,
		"resolution":              hongguoV5Resolution,
		"dpi":                     hongguoV5DPI,
		"version_code":            hongguoV5VersionCode,
		"version_name":            hongguoV5VersionName,
		"manifest_version_code":   hongguoV5VersionCode,
		"update_version_code":     hongguoV5VersionCode,
		"pv_player":               hongguoV5VersionCode,
		"language":                hongguoV5Language,
		"host_abi":                hongguoV5HostABI,
		"ac":                      "wifi",
		"dragon_device_type":      hongguoV5DragonDeviceType,
		"is_android_pad_screen":   hongguoV5PadScreen,
		"compliance_status":       "0",
		"need_personal_recommend": "1",
		"player_so_load":          "1",
	} {
		if got := hongguoV5QueryValue(t, query, key); got != want {
			t.Fatalf("query %s=%q，应为 %q", key, got, want)
		}
	}
	// 5) 身份三件套仍取自身份状态（不许写死）。
	for key, want := range map[string]string{
		"device_id": state.DeviceID, "iid": state.InstallID, "cdid": state.CDID,
	} {
		if got := hongguoV5QueryValue(t, query, key); got != want {
			t.Fatalf("query %s 不是身份状态里的值: got %q", key, got)
		}
	}
	// 6) 登记侧的方言一项都不许进 query。
	for _, banned := range []string{hongguoRegisterVersion, state.DeviceModel, state.DeviceChannel} {
		if strings.Contains(query, banned) {
			t.Fatalf("query 里混进了登记侧取值 %q: %q", banned, query)
		}
	}

	// 7) UA 版本与 query 版本必须同属 v5 时代：两者都不得回落到登记侧的 7.3.5.32。
	userAgentVersion := hongguoV5UserAgentVersion(t, userAgent)
	uaValue, err := strconv.Atoi(userAgentVersion)
	if err != nil {
		t.Fatalf("UA 版本号不是数字: %q", userAgentVersion)
	}
	queryValue, err := strconv.Atoi(hongguoV5QueryValue(t, query, "version_code"))
	if err != nil {
		t.Fatalf("query version_code 不是数字")
	}
	const v5Floor = 73932
	if uaValue < v5Floor || queryValue < v5Floor {
		t.Fatalf("版本号回落到 v5 之前: ua=%d query=%d（下限 %d）", uaValue, queryValue, v5Floor)
	}
	registerValue, err := strconv.Atoi(hongguoRegisterVersion)
	if err != nil {
		t.Fatalf("登记版本号不是数字: %q", hongguoRegisterVersion)
	}
	if uaValue == registerValue || queryValue == registerValue {
		t.Fatalf("v5 请求仍在用登记侧的 App 版本 %d", registerValue)
	}

	// 8) 头清单：签名三件套 + 压缩戳 + 匿名头组 + 播放侧覆盖。
	for _, key := range []string{
		"X-Khronos", "X-Helios", "X-Medusa", "X-Ss-Stub",
		"X-SS-DP", "lc", "X-Xs-From-Web", "x-reading-request",
	} {
		if header.Get(key) == "" {
			t.Fatalf("缺少 v5 必需头 %s", key)
		}
	}
	// 旧路径的签名头一个都不许带：它们会把这请求判成老客户端。
	for _, key := range []string{
		"X-Gorgon", "X-Argus", "X-Ladon", "X-Tt-Dt",
		"Sdk-Version", "Passport-Sdk-Version", "X-Vc-Bdturing-Sdk-Version", "X-Ss-Req-Ticket",
	} {
		if got := header.Get(key); got != "" {
			t.Fatalf("v5 请求不该带旧路径的头 %s=%q", key, got)
		}
	}
	if got := header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q，应为 gzip", got)
	}
	if got := header.Get("Accept"); got != hongguoV5Accept {
		t.Fatalf("Accept=%q，应为 %q", got, hongguoV5Accept)
	}
	if got := header.Get("lc"); got != "101" {
		t.Fatalf("lc=%q，应为 101", got)
	}
	if got := header.Get("X-Xs-From-Web"); got != "0" {
		t.Fatalf("x-xs-from-web=%q，应为 0", got)
	}
	// Cookie 只在登记下发了 ttreq 时才带，且内容与身份状态同源。
	cookie := header.Get("Cookie")
	if !strings.Contains(cookie, "ttreq="+state.TTReq) || !strings.Contains(cookie, "install_id="+state.InstallID) {
		t.Fatalf("Cookie 与身份状态不同源: %q", cookie)
	}

	// 9) 信封里的设备档案必须与 query 同源（用 profile 反查一次，防有人只改一半）。
	if profile.Model != hongguoV5DeviceModel || profile.Brand != hongguoV5DeviceBrand ||
		profile.OSVersion != hongguoV5OSVersion || profile.OSAPI != hongguoV5OSAPI ||
		profile.Resolution != hongguoV5Resolution || profile.DPI != hongguoV5DPI ||
		profile.Channel != hongguoV5Channel || profile.Build != hongguoV5Build ||
		profile.VersionCode != hongguoV5VersionCode || profile.VersionName != hongguoV5VersionName {
		t.Fatalf("信封设备档案与 query 不同源: %+v", profile)
	}
	if profile.DeviceID != state.DeviceID || profile.InstallID != state.InstallID || profile.CDID != state.CDID {
		t.Fatalf("信封里的身份三件套没有取自身份状态: %+v", profile)
	}
	// 信封 field 31/34 的品牌与 query 的 device_brand 故意不同源（上游写死 HONOR）。
	if hongguoV5EnvelopeBrand == hongguoV5DeviceBrand {
		t.Fatal("信封品牌与 device_brand 不该相同，上游把 31/34 写死成 HONOR")
	}
}

// TestHongguoV5BranchUsesDigests 钉住分支门禁的入参口径。
//
// 上游是 getIV(getIV(getIV(0x20230928, sm3(query)), md5(body)), le32(ts))：前两段
// 吃的是**摘要**。喂原文时 nibble 只有 1/16 的概率与服务端撞上，其余 15/16 会被
// 静默拒绝（HTTP 200 + 0 字节），这正是「换号从来没用」的原因。
func TestHongguoV5BranchUsesDigests(t *testing.T) {
	const query = "device_platform=android&os=android&ssmix=a&_rticket=1764550000123&device_id=7702409114256690238"
	body := []byte{0x1f, 0x8b, 0x08, 0x00, 0xd3, 0x2a, 0x1c, 0x67, 0x00, 0x03}
	const stamp uint32 = 1764550000

	digestStyle := hongguoV5BranchOf(query, body, stamp)

	// 旧口径（喂原文）在同样的输入上必须算出不同的 nibble，证明改动真的生效。
	rawStyle := hongguoV5SigningIV(0x20230928, []byte(query))
	rawStyle = hongguoV5SigningIV(rawStyle, body)
	rawStyle = hongguoV5SigningIV(rawStyle, hongguoV5LE32(stamp)) & 15
	if digestStyle == rawStyle {
		t.Fatalf("摘要口径与原文字口径算出了同一个 nibble(%d)，说明分支判定没改到", digestStyle)
	}

	// 再按上游式子手算一遍：两段入参分别是 sm3 与 md5 的摘要，且长度必须正确。
	queryHash := hongguoSM3([]byte(query))
	bodyHash := md5.Sum(body)
	manual := hongguoV5SigningIV(0x20230928, queryHash[:])
	manual = hongguoV5SigningIV(manual, bodyHash[:])
	manual = hongguoV5SigningIV(manual, hongguoV5LE32(stamp)) & 15
	if digestStyle != manual {
		t.Fatalf("分支 nibble 与手算不一致: got %d want %d", digestStyle, manual)
	}
	if digestStyle > 15 {
		t.Fatalf("nibble 越界: %d", digestStyle)
	}
}

// TestHongguoV5BranchReachesTwelve 证明扫 _rticket 确实能命中分支 12。
// 分支口径错了会扫满 1024 次仍不命中（上游同样会报错），这条能兜住那种回归。
func TestHongguoV5BranchReachesTwelve(t *testing.T) {
	profile := hongguoV5DeviceFromState(hongguoV5TestState())
	body, err := hongguoV5Gzip([]byte(`{"probe":1}`))
	if err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	now := time.Unix(1764550000, 0)
	query, err := hongguoV5TuneTicket(profile, body, now)
	if err != nil {
		t.Fatalf("扫不到分支 12: %v", err)
	}
	if got := hongguoV5BranchOf(query, body, uint32(now.Unix())); got != hongguoV5Branch {
		t.Fatalf("命中的 query 分支号是 %d，应为 %d", got, hongguoV5Branch)
	}
}

// TestHongguoV5EmptyTicketOmitted 守住「没有 ttreq 就不带 Cookie」这条，
// 免得空票据被当成一个字段发上去。
func TestHongguoV5EmptyTicketOmitted(t *testing.T) {
	state := hongguoV5TestState()
	state.TTReq = ""
	state.StoreRegion = ""
	state.StoreRegionSrc = ""
	body, err := hongguoV5Gzip([]byte(`{"probe":1}`))
	if err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	profile := hongguoV5DeviceFromState(state)
	_, header, err := hongguoV5Headers(profile, body, time.Unix(1764550000, 0))
	if err != nil {
		t.Fatalf("生成 v5 请求头失败: %v", err)
	}
	if cookie := header.Get("Cookie"); cookie != "" {
		t.Fatalf("没有 ttreq 时不该带 Cookie: %q", cookie)
	}
}
