package core

// 红果 v5 取流签名（2026-11-16 站方下线旧取址接口后唯一可用的一套）。
//
// 背景：`/novel/player/multi_video_model/v1/` 与 `video_model/v1/` 在 2026-11-16
// 起统一返回 HTTP 200 + 0 字节（软停服）。现行入口是
// `POST /novel/player/multi_video_model/player/v1`，body 必须是 **gzip 压缩后的字节**，
// 且请求头只认 v5 三元组 `x-khronos` / `x-helios` / `x-medusa`——带旧的
// Argus 551 / Gorgon / Ladon 反而无效。
//
// 三套签名互不兼容，且都要求「被签的字节 == 实际发出的字节」，所以：
//   - query 必须是**最终发出的那一串**（含 _rticket），不能签完再改；
//   - body 必须是**压缩后**的字节（x-ss-stub 也是压缩后的 MD5）；
//   - v5 的 Medusa 有个分支号：query+body+ts 三者算出的低 4 位必须等于 12，
//     否则必须换一个 _rticket 重算（playbackV5Tune 的做法就是挨个扫）。
//
// 算法移植来源：ZainCheung/smovie internal/hongguo/{sign.go,sign_v5.go} 与
// aston314/hongguo-tv core/cmd/hongguotv/playback_v5_sign.go（两份独立 Go 移植，
// 交叉核对无差异），其上游是 N3urda/hongguoTV 的 Signer.kt / AesV3.kt / V5Constants.kt。

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// hongguoV5Path 是现行取流入口。注意**没有**结尾斜杠，与旧入口不同。
	hongguoV5Path = "/novel/player/multi_video_model/player/v1"
	// hongguoV5License 是 v5 专用 license，与旧 551 的 1611921764 完全不是一回事，
	// 串错只会拿到空体（Helios 的明文里就带着它）。
	hongguoV5License = "646637794"
	// hongguoV5ProtoVersion 是随请求上报的 SDK 版本串，必须与 v5 常量集一致。
	hongguoV5ProtoVersion = "v04.10.03-ml-android"
	hongguoV5EnvVersion   = "v04.10.03.04-bugfix"
	// hongguoV5SDKVersionCode 是消息体 field9，0x040a0300 对应 v04.10.03。
	hongguoV5SDKVersionCode = 0x040a0300
	// v5 客户端上报的版本号必须与 SDK 串配套：v04.10.03 是 7.4.x 的 SDK，
	// 用登记那套 7.3.5.32 会出现「7.3.5 客户端装着 7.4.10 SDK」这种不可能的组合。
	hongguoV5VersionCode = "74332"
	hongguoV5VersionName = "7.4.3.32"
	// hongguoV5Branch 是 Medusa 支持的唯一分支号（signingIV 链低 4 位）。
	hongguoV5Branch = 12
	// hongguoV5TicketSpan 是 _rticket 的搜索窗口：1 秒内必然能扫到分支 12。
	hongguoV5TicketSpan = 1024

	// 下面这组是 v5 请求的**设备档案**，全部固定，不取登记机型。
	//
	// 取值来源：beilunyang/openhongguo **v0.3.1**（2026-11-18T01:18 的
	// "fix(playback): send the V5 request the way the 7.4.3.32 client does"）：
	// sdk/model/device.go 的 `model.DefaultDevice` + internal/protocol/playback_v5.go。
	// 那一版把整条请求形状对齐到 7.4.3.32 客户端：query 29 项、_rticket 落第 4 槽、
	// UA 是 Cronet 形态但版本号 74332、设备档案跟着它的 DefaultDevice 走。
	//
	// 只有 DeviceID / InstallID / CDID 仍取自身份状态（每次登记独有，写死等于所有
	// 安装共用一个设备）。
	hongguoV5Package     = "com.phoenix.read"
	hongguoV5DeviceModel = "23127PN0CC"
	hongguoV5DeviceBrand = "Xiaomi"
	hongguoV5OSVersion   = "14"
	hongguoV5OSAPI       = "34"
	hongguoV5Resolution  = "1200*2670"
	hongguoV5DPI         = "460"
	hongguoV5Channel     = "xiaomi_8662_64"
	hongguoV5Build       = "UKQ1.230804.001"
	// hongguoV5EnvelopeBrand 是信封 field 31 / 34 里的品牌串。
	// v0.3.1 的 medusa 把这两位**写死成 HONOR**（与 query 的 device_brand 不同源），
	// 照抄，否则签名自洽性与上游对不上。
	hongguoV5EnvelopeBrand = "HONOR"
	// hongguoV5Accept 是 v5 播放请求的 Accept 原文（带 protobuf 后缀）。
	hongguoV5Accept = "application/json; charset=utf-8,application/x-protobuf"
	// 对应 DefaultDevice 的非平板形态（dragon_device_type / is_android_pad_screen）。
	hongguoV5DragonDeviceType = "phone"
	hongguoV5PadScreen        = "0"
	// hongguoV5HostABI 与 language 是客户端的固定取值。
	hongguoV5HostABI  = "arm64-v8a"
	hongguoV5Language = "zh"
)

// hongguoV5UserAgent 拼 v5 播放请求的 UA。
//
// 形态是 App 的 Cronet UA（与登记侧同一个模板），但**版本号必须是 74332**：
// v5 信封里的 SDK 是 v04.10.03，对应 7.4.x。用登记侧的 73532 会造出
// 「7.3.5 客户端装着 7.4.3 SDK」这种线上不存在的指纹，服务端统一回 200 + 0 字节。
// 模板逐字来自 openhongguo v0.3.1 的 protocol.PlaybackUserAgent。
func hongguoV5UserAgent() string {
	return fmt.Sprintf("%s/%s (Linux; U; Android %s; zh_CN; %s; Build/%s; Cronet/TTNetVersion:8d40f833 2026-03-03 QuicVersion:21ac1950 2025-11-18)",
		hongguoV5Package, hongguoV5VersionCode, hongguoV5OSVersion, hongguoV5DeviceModel, hongguoV5Build)
}

var (
	hongguoV5Magic   = hongguoV5Unhex("2d4b4fca49750d433fb5ae2c226dcc56")
	hongguoV5SignKey = hongguoV5Unhex("b23c2a470a20d105dc3f8c4438f8272ed46d6c89ffdcdedd5108698d26e9489d")
	// hongguoV5SBox 是 v5 专用的 4×256 分组密码 sbox，和旧 Medusa 那份不是同一组字节。
	hongguoV5SBox = hongguoV5Unhex(
		"53290fe9e51f316f90f74a7e034d367784ab49237cd0135cdc632c322fd3be0e2624a417ecd9151a7ff64f60730a87f9445ec6ada2f865d22b696c4188f24c0d" +
			"f37b75510b939f6bc2fe6883e68bc8c722181ba0bdb08c9871b50c9a16b46eea8eed20ba4b8a14e0fcf1468dbbef05a93e5a9d7dcf06898635c1fd99f5de287a" +
			"80c594daaab19b6279fbd75070cb52eb2555c456e39e7839853874e2f0013ac0b64742d8ca10e4b79c955de8b843ce4ed464a6a72ed508091c92e7b3eedd1e1d" +
			"d12afac321618fa3d65f9167c982f43d66a53c6a1997585b346dbf9672ff37cdbc073b2dae11453304df00815476b2a13fccdb30b957a8acafe1274002485912" +
			"38922563e36406d37a24100b7902a028044e218453c2a399199c9d13dc40c55ba67e502273850ebdc1a2e2188b8f2d9e3cb909b8c46a2a9f4a6dd93007eafa65" +
			"b7b3a881aa908cc74f0ad76bbbb58617c37012ca05a469fd44e62eecf1e4272ce860c0ae5eadf99b20d8f0234174006f3f110d7657d01645d4eff8f71471cbfe" +
			"1cb2f52648727bf3c9de55153e031b4d61757db63dbf7782eeafe9018a8891362fb14759ac32bab03b349aeb5a377c5156be4c8dfbe13a684bda39cf836697c8" +
			"94545fe0f2491acea9fc6252f4ff1e0fe72bd2df6e67a7f635e5bcc6d1a5dd951d89960887a18eb40c785cdb1f80ed42299333ab98cdd67f316cd54346585dcc" +
			"d759fb722789b08807cc0fda293f7e5e1819bb0353517020b31d3d00374952bea2c421e862040a9ec892a05b266b7a0d0cc316f483b81af39715657ffe32ad8d" +
			"e0c13077646035236ee5363e5f900e9a7cf850a81cd9960b6df73317226f99eaa6fff15cec34acb98246639b918ab2851466a7cdfc4ad1b52c2ae95a58d3734e" +
			"47ddd5b7560193f644ef9c408b7dee39b4d6aabf4de761e3252d4c7ba3843187745d55e6a5db05d42b45caed43c0c5d0b64bfa9d94a4a9d8f2f0e1a108dcfd28" +
			"8fd2bc1fc7bd675412138169cb09249875ba3b68cfb11180afae104fc9789571c6dfce02413886796ae21e3c488cc21b9f423a066c2eabf98e2fdeeb5776e4f5" +
			"d09a515ba58cbbab3e37b09fd4f6d9244df80512e84cd7956be301cf64f1429d58577cc241f3a2c655144f81aaf95a4e108da894b38970561ae9002619fbbac9" +
			"1fa922ed202bfc62cb08e0de2a7f3631435f8b47c8a3c563b4db4071ce74c46da6fa696a9e282d1e825d663b93bfd56c48dce1884bb10fccea063929f746443f" +
			"d830a4232e53da21a1f2d1bdfebc3a02c10a1b8ad31d04fd616590d272ac737e98eb33af60528716bee78ec0ee8699031177277d9cff32542c67caf5f4459168" +
			"1335ae0e178f3409a7803db83892dde5b9b7f0c750e24aef077badd678a09b850b84ec1597e4963cb5e675df1c0d6fb618496ecd76c35c798325590c2fb27a5e")
)

// hongguoV5HashTable 是 F13 压缩函数每 4 轮取一次的常数表（v5 与旧 profile 共用同一份）。
var hongguoV5HashTable = [64]uint32{
	0xebb64faf, 0x07aadcc2, 0xcf3187bf, 0xe01138ff, 0x6d0bfcff, 0x5a30a3be, 0xb41ad638, 0x34180eb8,
	0xf233eb6f, 0xb1a584cc, 0xccc30dc7, 0x47d1db51, 0xd55653de, 0x70a84fa1, 0x57473c12, 0xf76f0288,
	0x2c077f0a, 0xda0dcad0, 0xfbb86f6c, 0xfdc4cf00, 0x688a020d, 0xe676c6a6, 0x8cd6338b, 0x1a3c8d0e,
	0xcce8b06b, 0x6ad0ed0b, 0xa0522717, 0xdc71ac83, 0x2285db71, 0xd5b4dda6, 0x736f8650, 0x6560306c,
	0x617ce2a6, 0xe423417e, 0x0a40e143, 0x544e4032, 0x88dffb2a, 0x716c1ae0, 0x4c467a88, 0x05b23bb3,
	0xe1d0b866, 0xbaa3dcb8, 0xae3374d3, 0xc3381a50, 0x1702f75b, 0xfe6da368, 0xf0b4cf48, 0x4e0ffbb8,
	0x72aad10d, 0x26c53a3d, 0xf2bce0f6, 0xb4557581, 0x4a257fdd, 0x8c3182a2, 0xab0b3b86, 0x3d5dfb14,
	0x4f103634, 0xd37b52d7, 0x444eff16, 0xeb0a33d1, 0x6ca86f6e, 0x00284ba7, 0x08387cfa, 0x5fb37586,
}

// hongguoV5Process 是同一进程内固定的两个值：SDK 会话随机串与启动时间。
// 它们在同一个 App 生命周期里**不会变**，每次请求重新生成比复用更容易被判异常。
var hongguoV5ProcessNonce = func() string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}()

var hongguoV5ProcessStarted = time.Now().UnixMilli()

// hongguoV5Profile 描述一次 v5 请求里所有「参与签名又要发给服务端」的字段。
// 设备档案（版本 / 机型 / 品牌 / 系统 / 分辨率 / 渠道 / UA）一律取 v5 常量集，
// 只有设备身份三件套取自身份状态，见 hongguoV5DeviceFromState。
type hongguoV5Profile struct {
	DeviceID    string
	InstallID   string
	CDID        string
	VersionCode string
	VersionName string
	Channel     string
	Model       string
	Brand       string
	OSVersion   string
	OSAPI       string
	Resolution  string
	DPI         string
	Build       string
	UserAgent   string
	// State 是登记侧的原始身份。它只用于两件与签名无关的事：
	// Cookie 头里的 ttreq / store-region，以及 x-reading-request 的时间戳。
	State hongguoIdentityState
}

// hongguoV5DeviceFromState 还原 v5 请求的设备描述。
//
// 设备档案全部固定（hongguoV5UserAgent() / Model / Brand / OSVersion / OSAPI /
// Resolution / DPI / Build / Channel），**不再**沿用登记时的机型。原因有两条：
//  1. 上游三份实现都是写死这套，且查询串、信封、UA 三者逐字段一致；
//  2. 登记侧是 7.3.5.32 的客户端（Cronet UA），v5 信封是 7.4.3.32 + v04.10.03 SDK。
//     把登记机型与 Cronet UA 搬进 v5 请求，就会出现「7.3.5 的客户端装着 7.4.3 的
//     SDK」这种不可能的指纹，服务端统一回 HTTP 200 + 0 字节。
//
// DeviceID / InstallID / CDID 仍取自身份状态：它们是每次登记独有的，写死等于所有
// 安装共用一个设备。
func hongguoV5DeviceFromState(state hongguoIdentityState) hongguoV5Profile {
	return hongguoV5Profile{
		DeviceID:    state.DeviceID,
		InstallID:   state.InstallID,
		CDID:        state.CDID,
		VersionCode: hongguoV5VersionCode,
		VersionName: hongguoV5VersionName,
		Channel:     hongguoV5Channel,
		Model:       hongguoV5DeviceModel,
		Brand:       hongguoV5DeviceBrand,
		OSVersion:   hongguoV5OSVersion,
		OSAPI:       hongguoV5OSAPI,
		Resolution:  hongguoV5Resolution,
		DPI:         hongguoV5DPI,
		Build:       hongguoV5Build,
		UserAgent:   hongguoV5UserAgent(),
		State:       state,
	}
}

// hongguoV5Pairs 按 7.4.3.32 客户端实际发送的顺序给出 v5 请求的 29 项 query。
//
// 三处不能动：
//   - **顺序**参与签名（SM3 对整串摘要），所以不能用 url.Values 的字母序；
//   - `_rticket` 必须留在**第 4 位**（0 基索引 3），值由签名阶段就地填入；
//   - `ssmix=a`、`ac=wifi`、`player_so_load=1`、`compliance_status=0` 这些看着像
//     噪音的项，缺席时服务端会把请求归到另一条（不下发码流的）分支。
//
// 逐项对应 openhongguo v0.3.1 的 protocol.PlaybackV5Pairs。
func (profile hongguoV5Profile) pairs() [][2]string {
	return [][2]string{
		{"device_platform", "android"},
		{"os", "android"},
		{"ssmix", "a"},
		{"_rticket", ""},
		{"cdid", profile.CDID},
		{"channel", profile.Channel},
		{"aid", hongguoPlaybackAID},
		{"app_name", "novelread"},
		{"version_code", profile.VersionCode},
		{"version_name", profile.VersionName},
		{"manifest_version_code", profile.VersionCode},
		{"update_version_code", profile.VersionCode},
		{"resolution", profile.Resolution},
		{"dpi", profile.DPI},
		{"device_type", profile.Model},
		{"device_brand", profile.Brand},
		{"language", hongguoV5Language},
		{"os_api", profile.OSAPI},
		{"os_version", profile.OSVersion},
		{"ac", "wifi"},
		{"host_abi", hongguoV5HostABI},
		{"dragon_device_type", hongguoV5DragonDeviceType},
		{"pv_player", profile.VersionCode},
		{"compliance_status", "0"},
		{"need_personal_recommend", "1"},
		{"player_so_load", "1"},
		{"is_android_pad_screen", hongguoV5PadScreen},
		{"iid", profile.InstallID},
		{"device_id", profile.DeviceID},
	}
}

// hongguoV5Escape 是 query 分量的转义。
//
// 比 url.QueryEscape 多保留 `-_.!~*'()` 这几个字符（官方客户端就是这么转的），
// 且**不会**把空格编成 `+`。签名摘要吃的是这串字节，多转一个字符就等于换了一个
// 请求，所以这里不能图省事交给 net/url。
func hongguoV5Escape(value string) string {
	const unreserved = "-_.!~*'()"
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || strings.IndexByte(unreserved, char) >= 0 {
			builder.WriteByte(char)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte("0123456789ABCDEF"[char>>4])
		builder.WriteByte("0123456789ABCDEF"[char&15])
	}
	return builder.String()
}

// hongguoV5EncodePairs 把有序 pair 拼成 query 串。
//
// 拼出来的这一串同时用于**签名**和**发送**，中间不允许再经过任何编码器。
func hongguoV5EncodePairs(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for index, pair := range pairs {
		parts[index] = hongguoV5Escape(pair[0]) + "=" + hongguoV5Escape(pair[1])
	}
	return strings.Join(parts, "&")
}

// hongguoV5Payload 是 v5 取流的请求体（未压缩）。
// 键是 "1004"（短剧），与旧入口要求 "1" 不同；video_platform 固定 1024。
// device_level 是 1（不是旧入口的 3）——两份上游 Go 移植都是 1，写 3 会走另一条
// 服务端分支。
func hongguoV5Payload(videoID string) map[string]any {
	return map[string]any{
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 1, "disable_digg_stat": false,
			"disable_video_relate_book": false, "need_all_video_definition": true,
			"need_mp4_align": false, "use_os_player": false, "use_server_dns": false,
			"video_platform": 1024,
		},
		"mixed_video_id_map": map[string]any{"1004": []string{videoID}},
		"dr_scene":           "player",
	}
}

// hongguoV5Gzip 压缩请求体。服务端校验的是**压缩后**的字节，
// x-ss-stub 与 x-medusa 都基于压缩再算，先签后压等于自毁。
func hongguoV5Gzip(payload []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(payload); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func hongguoV5Unhex(value string) []byte {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		panic("红果 v5 常量解码失败: " + value)
	}
	return decoded
}

func hongguoV5LE32(value uint32) []byte {
	return binary.LittleEndian.AppendUint32(nil, value)
}

func hongguoV5Random() (uint32, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(raw[:]), nil
}

// hongguoV5SigningIV 是签名用的滚动 IV：同样的三步链既决定 Medusa 分支号，
// 也是 F13 压缩函数的种子输入。low 4 位 == 12 才是 v5 支持的 Medusa 分支。
func hongguoV5SigningIV(seed uint32, data []byte) uint32 {
	for index, value := range data {
		if index&1 == 0 {
			seed = (seed >> 4) ^ seed ^ (seed << 6) ^ uint32(value)
		} else {
			seed = ^((seed >> 7) ^ seed ^ (uint32(value) | (seed << 12)))
		}
	}
	return seed
}

// hongguoV5BranchOf 算 Medusa 分支门禁的低 4 位。
//
// 上游式（internal/protocol/sign/playback_v5.go Sign）：
//
//	getIV(getIV(getIV(0x20230928, sm3(query)), md5(gzipBody)), le32(ts)) & 15 == 12
//
// 注意一、二两段的入参都是**摘要**，不是原文：第一段吃 sm3(query) 的 32 字节，
// 第二段吃 md5(压缩后 body) 的 16 字节。喂原文时链长从 48 字节变成两百多字节，
// 算出来的 nibble 与服务端重算的完全是两个数——服务端只有 1/16 的概率和我们撞上，
// 剩下 15/16 会被判成「分支不支持」而静默回 200 + 0 字节。
//
// hongguoV5SigningIV 与上游 getIV 逐行等价，这里只换入参，函数本身不动。
func hongguoV5BranchOf(query string, body []byte, stamp uint32) uint32 {
	queryHash := hongguoSM3([]byte(query))
	bodyHash := md5.Sum(body)
	value := hongguoV5SigningIV(0x20230928, queryHash[:])
	value = hongguoV5SigningIV(value, bodyHash[:])
	return hongguoV5SigningIV(value, hongguoV5LE32(stamp)) & 15
}

// hongguoV5TuneTicket 扫描 _rticket，直到三件套算出的分支号等于 12。
//
// `_rticket` 是**就地**填进第 4 槽的，不改参数顺序：上游 Sign() 先找到已有的
// `_rticket` 槽位再逐个 offset 覆写，只有完全找不到时才追加到末尾。
// 返回的这一串既是签名输入，也是最终发出去的 query——调用方不许再编码一次。
func hongguoV5TuneTicket(profile hongguoV5Profile, body []byte, now time.Time) (string, error) {
	return hongguoV5TunePairs(profile.pairs(), body, now)
}

// hongguoV5TunePairs 是 hongguoV5TuneTicket 的形态无关版本：给定任意一组有序 pair
// （旧 29 项、新 13 项都行），扫出一个能让 branch 命中的 `_rticket`。
//
// `_rticket` 槽位按**名字**定位，所以两套 query 的第 4 位 / 第 13 位差异不用特判；
// 槽位不存在时追加到末尾（与上游 Sign() 的行为一致）。
func hongguoV5TunePairs(pairs [][2]string, body []byte, now time.Time) (string, error) {
	stamp := uint32(now.Unix())
	base := now.UnixMilli()
	slot := -1
	for index := range pairs {
		if pairs[index][0] == "_rticket" {
			slot = index
			break
		}
	}
	if slot < 0 {
		pairs = append(pairs, [2]string{"_rticket", ""})
		slot = len(pairs) - 1
	}
	for offset := int64(0); offset < hongguoV5TicketSpan; offset++ {
		pairs[slot][1] = strconv.FormatInt(base+offset, 10)
		query := hongguoV5EncodePairs(pairs)
		if hongguoV5BranchOf(query, body, stamp) == hongguoV5Branch {
			return query, nil
		}
	}
	return "", errors.New("红果 v5 签名分支未命中")
}

// hongguoV5Checksum 是 F13 输出的 12 轮混淆校验和。
func hongguoV5Checksum(data []byte) uint32 {
	value := uint32(0x20220420)
	for index := 0; index < 12; index++ {
		if index&1 == 0 {
			value = uint32(data[index]) ^ (value << 7) ^ (value >> 3) ^ value
		} else {
			value = ^(uint32(data[index]) | (value << 11)) ^ (value >> 5) ^ value
		}
	}
	return (value | 4) ^ 0x1000000
}

// hongguoV5F13 是 v5 分支的 F13 压缩：与旧 profile 的差别是固定种子 0x9d4a97c2、
// 固定一轮 96 次、固定寄存器替换表，最后校验和额外或上 0x20。
func hongguoV5F13(queryHash, bodyHash []byte, stamp uint32) []byte {
	const seed uint32 = 0x9d4a97c2
	count := seed + stamp

	data := make([]byte, 0, 64)
	data = append(data, queryHash...)
	data = append(data, bodyHash...)
	data = append(data, hongguoV5LE32(stamp)...)
	// 这 4 字节是「上游 SDK 的随机盐位」，v5 分支下是常数序列。
	for _, value := range []uint32{0xfa, 0x45, 0x61, 0xd7} {
		data = append(data, byte((value|value<<8)>>((count+2)&7)))
	}
	data = append(data, 0, 0, 0, 0, 0, 0, 0x01, 0xa0)

	var schedule [128]uint32
	for index := 0; index < 16; index++ {
		schedule[index] = binary.BigEndian.Uint32(data[index*4:])
	}
	for index := 0; index < 112; index++ {
		left, right := schedule[index+1], schedule[index+14]
		mixed := bits.RotateLeft32(left, 14) ^ bits.RotateLeft32(left, 25) ^ (left >> 3)
		mixed += bits.RotateLeft32(right, 13) ^ bits.RotateLeft32(right, 15) ^ (right >> 10)
		schedule[index+16] = schedule[index] + schedule[index+9] + mixed
	}

	var table [64]uint32
	for index, value := range hongguoV5HashTable {
		table[index] = bits.RotateLeft32(value, -int((count+1)&31))
	}

	initial := [8]uint32{0x7aba4fc8, 0x67166507, 0x6403fa00, 0x340f512f, 984304912, 3005047866, 2874125293, 2152413264}
	for index := range initial {
		initial[index] = bits.RotateLeft32(initial[index], -int(count&31))
	}
	state := initial
	for index := uint32(0); index < 96; index++ {
		base := seed + index
		n1 := ((state[4] ^ state[0]) & state[1]) ^ state[4]
		n2 := bits.RotateLeft32(state[1], 26) ^ bits.RotateLeft32(state[1], 21) ^ bits.RotateLeft32(state[1], 7)
		n4 := schedule[base&127] + n1 + n2 + table[base&63] + state[6]
		n5 := bits.RotateLeft32(state[3], 30) ^ bits.RotateLeft32(state[3], 19) ^ bits.RotateLeft32(state[3], 10)
		n6 := (state[3] & state[5]) | ((state[3] | state[5]) & state[2])
		previous := state[7]
		copy(state[1:], state[:7])
		state[0] = previous + n4
		state[7] = n5 + n6 + n4
	}

	var digest [32]byte
	for index := range state {
		binary.BigEndian.PutUint32(digest[index*4:], state[index]+initial[index])
	}
	folded := make([]byte, 16)
	for index := range folded {
		folded[index] = digest[index] ^ digest[index+16]
	}
	return append(folded, hongguoV5LE32(hongguoV5Checksum(folded)|0x20)...)
}

// hongguoV5XMXor 是 Medusa 消息体的可逆混淆（先反序、再按 4 字节循环异或、
// 最后做三轮差分回填）。逐字节复刻上游，不可简化。
func hongguoV5XMXor(data, key []byte) []byte {
	out := make([]byte, len(data))
	for index, value := range data {
		at := (index * 4) & 28
		first, second := key[at], key[at+1]
		mixed := bits.RotateLeft8(value, 4) + first
		mixed = (^mixed) ^ second
		mixed = bits.RotateLeft8(mixed, 3)
		mixed += second
		mixed ^= first
		out[len(data)-index-1] = ^mixed
	}
	size := len(out)
	last := out[size-1] ^ out[size-2]
	head := out[0]
	out[0] = ^last + head
	out[1] = (out[0] ^ out[size-1] ^ 254) + out[1]
	out[2] += (last - head) ^ bits.RotateLeft8(out[1], 3) ^ 2
	for index := 0; index < size-4; index++ {
		mixed := bits.RotateLeft8(out[index+2], 3) ^ out[index+1] ^ byte(index+3)
		out[index+3] = ^mixed + out[index+3]
	}
	out[size-1] ^= out[size-2]
	var sum byte
	for _, value := range out[1:] {
		sum += value
	}
	out[0] = (out[0] ^ out[1]) + sum
	return out
}

// hongguoV5Field / hongguoV5Proto 是签名信封用的最小 protobuf 编码。
// sint 走 zigzag；uint32 直接乘 2（32 位机上 int 会掉符号，所以单独一支）。
type hongguoV5Field struct {
	tag   int
	value any
}

func hongguoV5SF(tag int, value any) hongguoV5Field {
	return hongguoV5Field{tag: tag, value: value}
}

func hongguoV5Proto(fields ...hongguoV5Field) []byte {
	out := make([]byte, 0, 256)
	for _, field := range fields {
		switch value := field.value.(type) {
		case int:
			out = binary.AppendUvarint(out, uint64(field.tag<<3))
			out = binary.AppendUvarint(out, uint64(int64(value)<<1)^uint64(int64(value)>>63))
		case int64:
			out = binary.AppendUvarint(out, uint64(field.tag<<3))
			out = binary.AppendUvarint(out, uint64(value<<1)^uint64(value>>63))
		case uint32:
			out = binary.AppendUvarint(out, uint64(field.tag<<3))
			out = binary.AppendUvarint(out, uint64(value)*2)
		case string:
			if value != "" {
				out = hongguoV5ProtoBytes(out, field.tag, []byte(value))
			}
		case []byte:
			out = hongguoV5ProtoBytes(out, field.tag, value)
		}
	}
	return out
}

func hongguoV5ProtoBytes(out []byte, tag int, value []byte) []byte {
	out = binary.AppendUvarint(out, uint64(tag<<3|2))
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}

// hongguoV5AES 是 v5 专用的 AesV3 载体：分组、轮数与混合列都按 v5 sbox 与
// v5 置换表走，与旧 Medusa 的载体不是同一套参数。
type hongguoV5AES struct {
	box         []byte
	permutation [4]int
	order       [16]int
	keys        [48]byte
	wire        [8]uint
	compact     [8]uint
}

func newHongguoV5AES(key []byte, stamp uint32) *hongguoV5AES {
	variant := stamp & 3
	carrier := &hongguoV5AES{
		box:         hongguoV5SBox[variant*256 : (variant+1)*256],
		permutation: [4][4]int{{2, 0, 3, 1}, {1, 3, 2, 0}, {3, 2, 1, 0}, {2, 3, 1, 0}}[variant],
		order: [4][16]int{
			{0, 9, 14, 15, 4, 13, 2, 7, 8, 1, 6, 3, 12, 5, 10, 11},
			{0, 9, 14, 11, 4, 13, 2, 3, 8, 1, 6, 15, 12, 5, 10, 7},
			{0, 9, 14, 3, 4, 13, 2, 15, 8, 1, 6, 11, 12, 5, 10, 7},
			{0, 9, 14, 15, 4, 13, 2, 3, 8, 1, 6, 11, 12, 5, 10, 7},
		}[variant],
		wire:    [8]uint{6, 5, 3, 4, 2, 7, 1, 0},
		compact: [8]uint{1, 7, 3, 6, 2, 0, 4, 5},
	}
	initial := [4]uint32{0xbbb5de72, 0xd4638d5b, 0x92a5f73b, 0x89f8a1c9}[variant]
	seed := hongguoV5LE32(initial)
	for index := 0; index < 16; index++ {
		carrier.keys[index] = seed[index%4] ^ key[index]
	}
	rounds := 8
	for index := 4; index < 12; index++ {
		at := 4 * (index - 1)
		a, b, c, d := carrier.keys[at], carrier.keys[at+1], carrier.keys[at+2], carrier.keys[at+3]
		if index&3 == 0 {
			a, b, c, d = byte(initial>>uint(rounds&24))^carrier.box[b], carrier.box[c], carrier.box[d], carrier.box[a]
		}
		rounds += 2
		carrier.keys[at+4] = a ^ carrier.keys[at-12]
		carrier.keys[at+5] = b ^ carrier.keys[at-11]
		carrier.keys[at+6] = c ^ carrier.keys[at-10]
		carrier.keys[at+7] = d ^ carrier.keys[at-9]
	}
	return carrier
}

func (carrier *hongguoV5AES) block(value []byte) []byte {
	var rows [4][4]byte
	for index := range rows {
		copy(rows[index][:], value[index*4:])
	}
	addRoundKey := func(round int) {
		for row := 0; row < 4; row++ {
			for column := 0; column < 4; column++ {
				rows[row][column] ^= carrier.keys[round*16+row*4+carrier.permutation[column]]
			}
		}
	}
	addRoundKey(0)
	for round := 1; round < 3; round++ {
		previous := rows
		for row := 0; row < 4; row++ {
			for column := 0; column < 4; column++ {
				rows[row][column] = carrier.box[previous[carrier.permutation[row]][column]]
			}
		}
		previous = rows
		for index, at := range carrier.order {
			rows[index/4][index%4] = previous[at/4][at%4]
		}
		if round == 1 {
			previous = rows
			for row := 0; row < 4; row++ {
				for column := 0; column < 4; column++ {
					rows[row][column] = previous[row][carrier.permutation[column]]
				}
			}
			xtime := func(value byte) byte {
				if value&128 != 0 {
					return value<<1 ^ 0x1b
				}
				return value << 1
			}
			for column := 0; column < 4; column++ {
				all := rows[0][column] ^ rows[1][column] ^ rows[2][column] ^ rows[3][column]
				head := rows[0][column]
				rows[0][column] ^= all ^ xtime(rows[0][column]^rows[1][column])
				rows[1][column] ^= all ^ xtime(rows[1][column]^rows[2][column])
				rows[2][column] ^= all ^ xtime(rows[2][column]^rows[3][column])
				rows[3][column] ^= all ^ xtime(rows[3][column]^head)
			}
		}
		addRoundKey(round)
	}
	out := make([]byte, 0, 16)
	for row := range rows {
		for column := range rows[row] {
			out = append(out, rows[row][column]^carrier.keys[16+row*4+column])
		}
	}
	return out
}

// encrypt 把明文按位重排进 248 位载荷，用两条 CBC 链生成密钥流，
// 再把密钥流写回原字节的特定位。返回 1 字节头 + 原长度的密文。
func (carrier *hongguoV5AES) encrypt(source, iv []byte) []byte {
	var plain [32]byte
	for index := 0; index < 31; index++ {
		for bit := 0; bit < 8; bit++ {
			plain[index] |= ((source[index*8+bit] >> carrier.wire[bit]) & 1) << carrier.compact[bit]
		}
	}
	plain[31] = 1
	var stream []byte
	previous := iv
	for index := 0; index < 32; index += 16 {
		block := append([]byte(nil), plain[index:index+16]...)
		for at := range block {
			block[at] ^= previous[at]
		}
		previous = carrier.block(block)
		stream = append(stream, previous...)
	}
	out := append([]byte(nil), source...)
	for index := 0; index < 31; index++ {
		for bit := 0; bit < 8; bit++ {
			at := index*8 + bit
			out[at] = out[at]&^(byte(1)<<carrier.wire[bit]) | ((stream[index]>>carrier.compact[bit])&1)<<carrier.wire[bit]
		}
	}
	return append([]byte{stream[len(stream)-1]}, out...)
}

// hongguoV5Helios 是短签名头：明文字面是 "ts-license-8662"，
// license 必须是 v5 的 646637794（与账号那套 1588093228 不同）。
func hongguoV5Helios(stamp, random uint32) string {
	digest := md5.Sum(append(hongguoV5LE32(random), []byte("8662")...))
	ascii := []byte(hex.EncodeToString(digest[:]))
	words := make([]uint64, 0, 4)
	for index := 0; index < 4; index++ {
		words = append(words, binary.LittleEndian.Uint64(ascii[index*8:]))
	}
	left, right := words[0], words[1]
	table := []uint64{left}
	words = words[2:]
	for index := 0; index < 34; index++ {
		mixed := (bits.RotateLeft64(right, -8) + left) ^ uint64(index)
		words = append(words, mixed)
		mixed ^= bits.RotateLeft64(left, -61)
		table = append(table, mixed)
		left, right = mixed, words[0]
		words = words[1:]
	}
	plain := hongguoCommentPad([]byte(strconv.FormatUint(uint64(stamp), 10) + "-" + hongguoV5License + "-8662"))
	out := hongguoV5LE32(random)
	for at := 0; at < len(plain); at += 16 {
		a, b := binary.LittleEndian.Uint64(plain[at:]), binary.LittleEndian.Uint64(plain[at+8:])
		for index := 0; index < 34; index++ {
			b = table[index] ^ (a + bits.RotateLeft64(b, -8))
			a = b ^ bits.RotateLeft64(a, -61)
		}
		out = binary.LittleEndian.AppendUint64(out, a)
		out = binary.LittleEndian.AppendUint64(out, b)
	}
	return base64.StdEncoding.EncodeToString(out)
}

// hongguoV5MessageField21 是 Medusa message 段 field 21 的取值，**必须是 312**。
//
// 这是 2026-11-22 起全线 200 + 0 字节的唯一根因。field 21 按 sint 编码（zigzag），
// 312 在 wire 上的原始 protobuf 数值是 624；仓库长期用的 377 会编成 754。
// 服务端把 754 判为「未知客户端档位」：路由照常命中、业务层直接返回空体，
// 所以从状态码与响应头上看不出任何异常。
//
// 实测（同一身份、同一 vid 7704580213098679358、同一秒内连发两发）：
//
//	377 → HTTP 200 /     0 字节  logid 2026112403374506621739302346A2B548
//	312 → HTTP 200 / 24234 字节  logid 202611240337450C241562455E829712A3
//	                                （video_height 1280，10 档码率变体）
//
// 换成 312 之后 path 用 preload/v1、multi/v1 还是 player/v1 都能出数据，
// device_level 用 1 或 2 也都能出——真正被判据的只有这一个字段。
//
// 来源：N3urda/hongguoTV docs/research/2026-11-23-issue102-nonweb-playback.md
// 与 kotlin-tv/core/src/main/kotlin/com/hongguotv/core/Signer.kt 的 medusaV5()。
// **不要再改回 377。**
//
// 需要按别的档位生成签名（只用于比对旧参考向量）时，走 hongguoV5MedusaWithTag
// 显式传入档位，**不要**改动这个常量。
const hongguoV5MessageField21 = 312

// hongguoV5Medusa 是长签名头：核心信封 + F13 + xmxor + AesV3 三层叠加。
// 任何一处偏差都只会得到 HTTP 200 + 0 字节，没有可读的报错。
func hongguoV5Medusa(profile hongguoV5Profile, query string, body []byte, stamp uint32) (string, error) {
	return hongguoV5MedusaWith(profile, query, body, stamp, hongguoV5ProcessNonce, hongguoV5ProcessStarted, hongguoV5Random)
}

// hongguoV5MedusaWith 是可注入随机源与会话值的版本：生产走上面的包装，
// 对照校验时用固定输入逐字节比对（上游参考实现也是这个形状）。
// 签名档位固定取生产值 hongguoV5MessageField21。
func hongguoV5MedusaWith(profile hongguoV5Profile, query string, body []byte, stamp uint32,
	nonce string, started int64, next func() (uint32, error)) (string, error) {
	return hongguoV5MedusaWithTag(profile, query, body, stamp, hongguoV5MessageField21, nonce, started, next)
}

// hongguoV5MedusaWithTag 在 hongguoV5MedusaWith 之上再多暴露一个签名档位参数。
//
// 存在的唯一理由是**参考向量比对**：golden 值是旧档（field 21 = 377）下的产物，
// 用它锚定算法本身（SM3 / F13 → xmxor → AesV3 → 信封）有没有漂移，与线上用哪个
// 档位无关。要改线上档位请改 hongguoV5MessageField21 常量并同步更新
// TestHongguoV5MessageField21IsCurrent，**不要**从调用方传别的值进生产链路。
func hongguoV5MedusaWithTag(profile hongguoV5Profile, query string, body []byte, stamp uint32,
	tag int, nonce string, started int64, next func() (uint32, error)) (string, error) {
	bodyHash := md5.Sum(body)
	queryHash := hongguoSM3([]byte(query))
	f13 := hongguoV5F13(queryHash[:], bodyHash[:], stamp)

	// deviceInfo 是 **14 个 tag** 的精简树，environment 是 **4 个 tag**。
	//
	// 社区流出的 miaoxiaohu profile 里另有一套「全量树」（device 约 30 个 tag，
	// 含 16~21 浮点、23/24/26~29/33/35/36/38；env 约 10 个 tag）。那是**另一个 wire
	// 变体**，与本仓库从 N3urda/kotlin-tv 对齐的 14+4 不是一回事。
	//
	// 本仓库一直用的就是 14+4，端到端实测可出 1080P，**不需要改**。把那套全量 tag
	// 搬进来会改变签名摘要，等于换了一个请求，只会退回 200 + 0 字节。
	// 已 A/B 证明的差异只有 message field 21（见 hongguoV5MessageField21）。
	deviceInfo := hongguoV5Proto(
		hongguoV5SF(1, 1), hongguoV5SF(2, 2), hongguoV5SF(3, hongguoPlaybackAID),
		hongguoV5SF(4, profile.DeviceID), hongguoV5SF(5, nonce),
		hongguoV5SF(6, "!noperm!"), hongguoV5SF(12, "Asia/Shanghai,8"), hongguoV5SF(13, "zh_CN"),
		hongguoV5SF(22, profile.OSVersion), hongguoV5SF(25, started),
		hongguoV5SF(30, profile.Model), hongguoV5SF(31, hongguoV5EnvelopeBrand),
		hongguoV5SF(32, profile.Model), hongguoV5SF(34, hongguoV5EnvelopeBrand),
	)
	environment := hongguoV5Proto(
		hongguoV5SF(6, hongguoV5EnvVersion), hongguoV5SF(8, profile.Channel),
		hongguoV5SF(12, deviceInfo), hongguoV5SF(14, profile.VersionName),
	)
	counters := hongguoV5Proto(hongguoV5SF(1, 1), hongguoV5SF(2, 1), hongguoV5SF(3, 1), hongguoV5SF(5, 1))
	flags := make([]byte, 8)

	messageRandom, err := next()
	if err != nil {
		return "", err
	}
	// message 的 tag 集合是 v5 专有的，**不要**按旧 medusa 的形状补字段。
	//
	// 旧 medusa 有、而 v5 明确**没有**的两个：
	//   - f(16)：进程随机 nonce。v5 只把它放在 device tag 5，不进 message；
	//   - f(19)：sm3(query + bodyMd5 + "none")。
	//
	// 这两个字段「看着更完整」，但只要补进去，query/body 摘要就变了，服务端立刻
	// 回到 HTTP 200 + 0 字节。同理 f(2)=5、f(9)=0x040a0300、f(15)=proto(1:1,2:1,3:1,5:1)、
	// f(20)="none"、f(24) 那段明文 json 都是 v5 指纹，改动等于换一个客户端。
	message := hongguoV5Proto(
		hongguoV5SF(1, hongguoV5Magic), hongguoV5SF(2, 5), hongguoV5SF(3, messageRandom),
		hongguoV5SF(4, hongguoPlaybackAID), hongguoV5SF(5, profile.DeviceID), hongguoV5SF(6, hongguoV5License),
		hongguoV5SF(7, profile.VersionName), hongguoV5SF(8, hongguoV5ProtoVersion),
		hongguoV5SF(9, int64(hongguoV5SDKVersionCode)), hongguoV5SF(10, flags),
		hongguoV5SF(12, stamp), hongguoV5SF(13, f13), hongguoV5SF(14, queryHash[:6]),
		hongguoV5SF(15, counters), hongguoV5SF(17, stamp), hongguoV5SF(20, "none"),
		hongguoV5SF(21, tag), hongguoV5SF(23, environment),
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

// hongguoV5Headers 一次性把 v5 三元组与配套头算齐。
// 返回的 query 就是最终必须原样发出去的那一串，调用方不允许再改。
func hongguoV5Headers(profile hongguoV5Profile, body []byte, now time.Time) (string, http.Header, error) {
	query, err := hongguoV5TuneTicket(profile, body, now)
	if err != nil {
		return "", nil, err
	}
	header, err := hongguoV5HeaderFor(profile, query, body, uint32(now.Unix()), now.UnixMilli())
	if err != nil {
		return "", nil, err
	}
	return query, header, nil
}

// hongguoV5HeaderFor 在 query 已定稿的前提下算出整套头。
//
// 头集合逐项对应 openhongguo v0.3.1 的 ResolvePlayback：签名头（x-khronos /
// x-helios / x-medusa / x-ss-stub）之上再叠匿名头组 anonymousHeaders
// （x-ss-dp / lc / x-reading-request / Cookie），然后显式覆盖成播放侧的
// UA、Accept、x-xs-from-web、Content-Encoding。
//
// 其中三处是 2026-11-18 那次全线空体之后才补上的：
//   - UA 必须是 7.4.3.32 的 Cronet 原文（hongguoV5UserAgent）；
//   - `lc: 101` 与 `x-xs-from-web: 0` 缺任意一项都会被归到非官方客户端；
//   - Cookie 只在登记下发了 ttreq 时才带（空票据带上去等于多一个空字段）。
func hongguoV5HeaderFor(profile hongguoV5Profile, query string, body []byte, stamp uint32, ticketMillis int64) (http.Header, error) {
	medusa, err := hongguoV5Medusa(profile, query, body, stamp)
	if err != nil {
		return nil, err
	}
	heliosRandom, err := hongguoV5Random()
	if err != nil {
		return nil, err
	}
	bodySum := hongguoV5BodySum(body)
	header := make(http.Header)
	header.Set("User-Agent", profile.UserAgent)
	header.Set("Accept", hongguoV5Accept)
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Content-Encoding", "gzip")
	header.Set("X-Ss-Stub", strings.ToUpper(hex.EncodeToString(bodySum[:])))
	header.Set("X-Khronos", strconv.FormatUint(uint64(stamp), 10))
	header.Set("X-Helios", hongguoV5Helios(stamp, heliosRandom))
	header.Set("X-Medusa", medusa)
	header.Set("X-SS-DP", hongguoPlaybackAID)
	header.Set("lc", "101")
	header.Set("X-Xs-From-Web", "0")
	header.Set("x-reading-request", hongguoPlaybackReadingRequest(ticketMillis))
	if profile.State.TTReq != "" {
		header.Set("Cookie", hongguoGuestCookie(profile.State))
	}
	return header, nil
}

func hongguoV5BodySum(body []byte) [16]byte {
	return md5.Sum(body)
}

// ============================================================================
// 7.4.5.32 形态（v1）：13 项 query、不带 `player` 段的入口。
// ============================================================================
//
// 2026-11-20 官方把取流的门禁切到了 7.4.5.32 客户端的请求形态，入口从
//
//	POST /novel/player/multi_video_model/player/v1   （29 项 query，2026-11-16 起的那套）
//
// 换成
//
//	POST /novel/player/multi_video_model/v1          （13 项 query，末尾无斜杠）
//
// 同一个 path 在 2026-11-16 之前就存在过，那时配的是「键 1」的 payload 与不签名的
// 发送方式；现在它配的是「键 1004」的 payload + 完整的 v5 三元组签名。
// 也就是说：**path 是老的，形态是新的**，两者必须成套使用。
//
// 三处与旧形态的硬差异，改任何一处都只会拿到 200 + 0 字节：
//   - query 只有 13 项（旧形态 29 项），且 `_rticket` 落在**最后一位**而不是第 4 位；
//   - UA 是 **73932** 的老 UA，**不跟着** query 里的 74532 走（这是官方客户端的
//     真实状态：装的是 7.4.5.32，但 TTNet 的 UA 尾巴还停在 7.3.9.32）；
//   - `Accept` 只有 `application/json`，没有旧形态那个 `,application/x-protobuf`；
//     并且**不带** Cookie / lc / x-reading-request / x-xs-from-web
//     （上游实现用 CookieJar.NO_COOKIES，带上去反而多出四个不该有的字段）。
//
// 签名材料一个字节都没变：signKey / sbox / magic / license 646637794 / 分支号 12
// 全部沿用。7.4.5.32 与 7.4.3.32 的 metasec 材料逐字节相同，换版本号不需要换材料。
//
// 算法来源：tsukiyo-zero/hongguoTV commit 573d75a 的
// kotlin-tv/core/src/main/kotlin/com/hongguotv/core/MobilePlayback.kt（11-20 修好）。
const (
	// hongguoV5V1Path 是 7.4.5.32 形态的入口。**末尾没有斜杠**，与旧入口不同。
	hongguoV5V1Path = "/novel/player/multi_video_model/v1"

	// 版本三件套：query 的 version_code / version_name / update_version_code。
	hongguoV5V1VersionCode = "74532"
	hongguoV5V1VersionName = "7.4.5.32"
	// 渠道串带 aid 后缀，与旧形态的 xiaomi_8662_64 同构。
	hongguoV5V1Channel = "vivo_8662_64"

	// 设备档案：vivo PGT-AN10（P60 的渠道商机型串），Android 12。
	hongguoV5V1DeviceModel  = "PGT-AN10"
	hongguoV5V1DeviceBrand  = "vivo"
	hongguoV5V1OSVersion    = "12"
	hongguoV5V1OSAPI        = "31"
	hongguoV5V1Resolution   = "1260*2800"
	hongguoV5V1DPI          = "480"
	hongguoV5V1Build        = "V417IR"

	// hongguoV5V1Accept 只有这一个 MIME，没有 protobuf 后缀。
	hongguoV5V1Accept = "application/json"
)

// hongguoV5V1UserAgent 是 7.4.5.32 客户端的 UA **原文**。
//
// 版本号是 **73932**，不能跟着 query 的 74532 改：官方客户端装的是 7.4.5.32，
// 但内嵌 TTNet 的 UA 尾巴停在 7.3.9.32。把两处改成一致会造出一个线上不存在的
// 指纹，服务端回 200 + 0 字节。这一条与旧形态（UA 版本号必须等于 query 版本号）
// 的规则**正好相反**，是最容易改错的地方。
const hongguoV5V1UserAgent = "com.phoenix.read/73932 (Linux; U; Android 12; zh_CN_#Hans; PGT-AN10; Build/V417IR;tt-ok/3.12.13.20)"

// hongguoV5V1Pairs 按官方客户端实际发送的顺序给出 13 项 query。
//
// 两处不能动：
//   - **顺序**参与 SM3 摘要，所以不能用 url.Values 的字母序；
//   - `_rticket` 必须在**最后一位**（0 基索引 12），值由 hongguoV5TunePairs 就地填入。
//
// 这 13 项里没有 device_brand / os_api / resolution / dpi / host_abi / ac / ssmix /
// manifest_version_code / pv_player / compliance_status 等等——旧形态那 29 项里
// 有一大半在这个形态下根本不上报，补上去等于换了一个请求。
func hongguoV5V1Pairs(profile hongguoV5Profile) [][2]string {
	return [][2]string{
		{"aid", hongguoPlaybackAID},
		{"app_name", "novelread"},
		{"version_code", profile.VersionCode},
		{"version_name", profile.VersionName},
		{"channel", profile.Channel},
		{"device_platform", "android"},
		{"device_type", profile.Model},
		{"os_version", profile.OSVersion},
		{"update_version_code", profile.VersionCode},
		{"iid", profile.InstallID},
		{"cdid", profile.CDID},
		{"device_id", profile.DeviceID},
		{"_rticket", ""},
	}
}

// hongguoV5V1DeviceFromState 还原 7.4.5.32 形态的设备描述。
//
// DeviceID / InstallID / CDID 取自身份状态（每次登记独有），其余全部取 v1 常量集：
// 这套档案与登记时随机选的机型**不一致**是正常的——登记走的是 7.3.5.32 的身份链路，
// 取流走的是 7.4.5.32 的播放链路，两边本来就是两台"不同的客户端"。
func hongguoV5V1DeviceFromState(state hongguoIdentityState) hongguoV5Profile {
	return hongguoV5Profile{
		DeviceID:    state.DeviceID,
		InstallID:   state.InstallID,
		CDID:        state.CDID,
		VersionCode: hongguoV5V1VersionCode,
		VersionName: hongguoV5V1VersionName,
		Channel:     hongguoV5V1Channel,
		Model:       hongguoV5V1DeviceModel,
		Brand:       hongguoV5V1DeviceBrand,
		OSVersion:   hongguoV5V1OSVersion,
		OSAPI:       hongguoV5V1OSAPI,
		Resolution:  hongguoV5V1Resolution,
		DPI:         hongguoV5V1DPI,
		Build:       hongguoV5V1Build,
		UserAgent:   hongguoV5V1UserAgent,
		State:       state,
	}
}

// hongguoV5V1Payload 是 7.4.5.32 形态的请求体（未压缩）。
//
// 与旧形态只差一项：**没有 `dr_scene`**。多带一个字段就是另一个请求。
// 键仍是 "1004"（短剧档位），video_platform 仍是 1024，device_level 仍是 1。
func hongguoV5V1Payload(videoID string) map[string]any {
	return map[string]any{
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 1, "disable_digg_stat": false,
			"disable_video_relate_book": false, "need_all_video_definition": true,
			"need_mp4_align": false, "use_os_player": false, "use_server_dns": false,
			"video_platform": 1024,
		},
		"mixed_video_id_map": map[string]any{"1004": []string{videoID}},
	}
}

// hongguoV5V1Headers 一次性把 7.4.5.32 形态的 query 与头算齐。
// 返回的 query 就是最终必须原样发出去的那一串，调用方不允许再改。
func hongguoV5V1Headers(profile hongguoV5Profile, body []byte, now time.Time) (string, http.Header, error) {
	query, err := hongguoV5TunePairs(hongguoV5V1Pairs(profile), body, now)
	if err != nil {
		return "", nil, err
	}
	header, err := hongguoV5V1HeaderFor(profile, query, body, uint32(now.Unix()))
	if err != nil {
		return "", nil, err
	}
	return query, header, nil
}

// hongguoV5V1HeaderFor 在 query 已定稿的前提下算出整套头。
//
// 只发 8 个头：4 个载体头（UA / Accept / Content-Type / Content-Encoding）
// + x-ss-stub（gzip 后 body 的 MD5）
// + 三个签名头（x-khronos / x-helios / x-medusa）。
//
// 刻意**不发** Cookie / lc / x-reading-request / x-xs-from-web：上游实现是
// CookieJar.NO_COOKIES，这四个头属于旧形态（player/v1）的客户端画像，
// 混进新形态等于同时冒充两代客户端。
func hongguoV5V1HeaderFor(profile hongguoV5Profile, query string, body []byte, stamp uint32) (http.Header, error) {
	medusa, err := hongguoV5Medusa(profile, query, body, stamp)
	if err != nil {
		return nil, err
	}
	heliosRandom, err := hongguoV5Random()
	if err != nil {
		return nil, err
	}
	bodySum := md5.Sum(body)
	header := make(http.Header)
	header.Set("User-Agent", profile.UserAgent)
	header.Set("Accept", hongguoV5V1Accept)
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Content-Encoding", "gzip")
	header.Set("X-Ss-Stub", strings.ToUpper(hex.EncodeToString(bodySum[:])))
	header.Set("X-Khronos", strconv.FormatUint(uint64(stamp), 10))
	header.Set("X-Helios", hongguoV5Helios(stamp, heliosRandom))
	header.Set("X-Medusa", medusa)
	return header, nil
}
