package core

// 红果取流接口（/novel/player/video_model/v1/）的请求签名。
//
// 仓库里同时存在三套互不兼容的 App 签名，各自的常量一个字节都不能串：
//
//	目录 / 详情 : provider_hongguo_player_sign.go，key ac1adaae…、proto v04.07.01-ml-android
//	弹幕        : provider_hongguo_comment_sign.go，aid=3019 那套
//	取流        : 本文件，Argus 551 信封，key fc78e0a9…、proto v04.04.05-ov-android
//
// 串错的表现一律是 HTTP 200 + 0 字节空体，从外面看和接口整体挂掉没有区别，
// 所以三份实现各自独立、常量不共享。
//
// 与另两套还有两个差别：
//   - 信封结构不同。取流用的是 551 版：protobuf 风格消息 → Simon 轮函数加密 →
//     掩码反转帧 → 整体字节反转 → AES-128-CBC（固定 key/iv，不由 Argus key 派生）。
//   - 头部组合按官方客户端补齐五件套：X-Gorgon / X-Khronos / X-Ss-Stub /
//     X-Argus / X-Ladon，外加 X-SS-Req-Ticket。缺一个头是被拒，多一个头只是浪费，
//     所以宁可照着官方补齐 —— 不能因为「某个探针少了某个头也能通」就推断它可省，
//     那些探针在所有变体下拿到的都是空体，证明不了任何多余头的必要性。
//     Gorgon 那段与端点无关（只吃 query/body 摘要和秒级时间戳），与目录/详情共用
//     provider_hongguo_player_sign.go 里的 hongguoGorgon，不另写一套。

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"
	"net/http"
	"strconv"
	"time"
)

const (
	// hongguoPlaybackVideoModelPath 是单集取流入口（跑在 hongguoPlaybackBaseURL 上），
	// 响应形态为 data.video_model。
	hongguoPlaybackVideoModelPath = "/novel/player/video_model/v1/"

	// hongguoPlaybackAID 是红果在头条体系里的 app id。query、Argus 与 Ladon 三处
	// 必须一致，任何一处写成别的 aid 都直接空体。
	hongguoPlaybackAID = "8662"

	// hongguoPlaybackSdkVersion 是信封里固定的 SDK 版本时间戳（1611921764），
	// 它与 query 里的 version_code 是两码事：一个跟着协议走，一个跟着 App 版本走。
	hongguoPlaybackSdkVersion = "1611921764"

	// hongguoPlaybackProtoVersion 是写进信封的协议版本串。换版本只意味着换信封布局，
	// 不能随手改成目录/详情那套 v04.07.01-ml-android。
	hongguoPlaybackProtoVersion = "v04.04.05-ov-android"

	// 以下三条是 Argus 551 的密钥材料，来自线上协议本身，改一个字节信封就废。
	hongguoPlaybackArgusKeyHex = "fc78e0a9657a0c748ce51559903ccf03510e51d3cff232d71343e88a321c5304"
	hongguoPlaybackAESKeyHex   = "d31e3718288a1027baab59f146a09a9c"
	hongguoPlaybackAESIVHex    = "ea180a0336ed352fcd24e4d50018ae54"
)

var (
	hongguoPlaybackArgusKey = hongguoPlaybackMustHex(hongguoPlaybackArgusKeyHex)
	hongguoPlaybackAESKey   = hongguoPlaybackMustHex(hongguoPlaybackAESKeyHex)
	hongguoPlaybackAESIV    = hongguoPlaybackMustHex(hongguoPlaybackAESIVHex)
)

// hongguoPlaybackMustHex 解析签名常量。常量都是包内字面量，解析失败只可能是
// 有人改错了字面量，属于开发期错误，直接在 init 阶段炸出来比线上悄悄空体好。
func hongguoPlaybackMustHex(value string) []byte {
	raw, err := hex.DecodeString(value)
	if err != nil {
		panic("红果取流签名常量不是合法十六进制: " + value)
	}
	return raw
}

// signHongguoPlaybackRequest 给取流请求打「Gorgon + 时间戳 + body 摘要 + Argus 551 + Ladon」。
//
// 调用约定（顺序错签名就废）：
//  1. 调用方先把 ts 与 _rticket 写进 URL.RawQuery；
//  2. 再调用本函数 —— 本函数读的是 request.URL.RawQuery 里**实际发出去的** query，
//     ts 必须与本函数用的 now 是同一时刻，否则 Argus 里的时间戳与 query 对不上。
//
// 所有头都用同一个 now：Gorgon 的秒级时间戳与 X-Khronos 必须同值，
// X-SS-Req-Ticket 是同一时刻的毫秒值。再取一次 time.Now() 就可能跨秒。
// body 可以为 nil（不带 body 的取流变体），此时不写 X-Ss-Stub。
// versionName 为空时回落到登记用的 App 版本，避免调用方漏传。
func signHongguoPlaybackRequest(request *http.Request, body []byte, now time.Time, deviceID, installID, versionName string) {
	if request == nil || request.URL == nil {
		return
	}
	stamp := uint32(now.Unix())
	query := request.URL.RawQuery
	if versionName == "" {
		versionName = hongguoRegisterVersionNa
	}
	nonce := hongguoPlaybackNonce(now)
	request.Header.Set("X-Khronos", strconv.FormatUint(uint64(stamp), 10))
	request.Header.Set("X-SS-Req-Ticket", strconv.FormatInt(now.UnixMilli(), 10))
	request.Header.Set("X-Gorgon", hongguoGorgon(query, body, stamp))
	if len(body) > 0 {
		request.Header.Set("X-Ss-Stub", fmt.Sprintf("%X", md5.Sum(body)))
	}
	request.Header.Set("X-Argus",
		hongguoPlaybackArgus551(query, body, stamp, nonce, hongguoPlaybackAID, deviceID, installID, versionName))
	request.Header.Set("X-Ladon", hongguoPlaybackLadon(stamp, nonce, hongguoPlaybackAID))
}

// hongguoPlaybackNonce 生成 Argus/Ladon 共用的 32 位随机数。
//
// 必须**一次生成、两处复用**：Argus 把它写进信封字段 3，Ladon 把它当作块密钥的种子，
// 两者不一致时签名自校验就过不去。取随机失败时退回纳秒时间戳，宁可弱一点也不要空签名。
func hongguoPlaybackNonce(now time.Time) uint32 {
	var seed [4]byte
	if _, err := rand.Read(seed[:]); err != nil {
		binary.LittleEndian.PutUint32(seed[:], uint32(now.UnixNano()))
	}
	return binary.LittleEndian.Uint32(seed[:])
}

// hongguoPlaybackReadingRequest 拼 x-reading-request 头。
//
// 格式固定为 "<毫秒时间戳>-<数字>"，与同一次请求的 _rticket 用同一个 ticket。
// 尾段服务端不参与校验（线上探针用过 445566、771122 等任意值），这里取随机值，
// 避免所有安装共用同一个常量而被当成同一客户端。
func hongguoPlaybackReadingRequest(ticket int64) string {
	var buffer [3]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return strconv.FormatInt(ticket, 10) + "-0"
	}
	value := int(buffer[0])<<16 | int(buffer[1])<<8 | int(buffer[2])
	return strconv.FormatInt(ticket, 10) + "-" + strconv.Itoa(value)
}

// hongguoPlaybackVarint 写一个 protobuf 风格的 varint 字段（field_number + 值）。
func hongguoPlaybackVarint(message []byte, field, value uint64) []byte {
	message = binary.AppendUvarint(message, field*8)
	return binary.AppendUvarint(message, value)
}

// hongguoPlaybackBytesField 写一个 protobuf 风格的 length-delimited 字段。
func hongguoPlaybackBytesField(message []byte, field uint64, value []byte) []byte {
	message = binary.AppendUvarint(message, field*8+2)
	message = binary.AppendUvarint(message, uint64(len(value)))
	return append(message, value...)
}

// hongguoPlaybackSimon 对 data 就地做 Simon 轮函数加密（72 轮、16 字节一块）。
//
// 轮密钥由 Argus key 前 32 字节展开，展开里的常量与位移都是协议固定的；
// data 必须是 16 的整数倍（调用前用 hongguoCommentPad 补齐）。
func hongguoPlaybackSimon(data []byte) {
	var keys [72]uint64
	for index := 0; index < 4; index++ {
		keys[index] = binary.LittleEndian.Uint64(hongguoPlaybackArgusKey[index*8:])
	}
	for index := 4; index < len(keys); index++ {
		mixed := bits.RotateLeft64(keys[index-1], -3) ^ keys[index-3]
		mixed ^= bits.RotateLeft64(mixed, -1)
		keys[index] = ^keys[index-4] ^ mixed ^ (uint64(0x3dc94c3a046d678b)>>uint((index-4)%62))&1 ^ 3
	}
	for offset := 0; offset < len(data); offset += 16 {
		left, right := binary.LittleEndian.Uint64(data[offset:]), binary.LittleEndian.Uint64(data[offset+8:])
		for _, round := range keys {
			left, right = right, left^(bits.RotateLeft64(right, 1)&bits.RotateLeft64(right, 8))^bits.RotateLeft64(right, 2)^round
		}
		binary.LittleEndian.PutUint64(data[offset:], left)
		binary.LittleEndian.PutUint64(data[offset+8:], right)
	}
}

// hongguoPlaybackArgus551 生成 Argus 551 信封。
//
// 结构：protobuf 风格消息 → Simon 加密 → 掩码反转帧 → 整体字节反转 →
// 补 0x61 0x6f 尾巴 → AES-128-CBC → base64（带 f2 81 前缀）。
// 其中 bodyHash / queryHash 都是「MD5 后的 SM3」取前 6 字节，SM3 复用仓库已有的
// hongguoSM3（provider_hongguo_comment_sign.go），不要另写一套。
func hongguoPlaybackArgus551(query string, body []byte, timestamp uint32, nonce uint32,
	aid, deviceID, installID, versionName string) string {
	bodyDigest := md5.Sum(nil)
	if len(body) > 0 {
		bodyDigest = md5.Sum(body)
	}
	bodyHash := hongguoSM3(bodyDigest[:])
	queryHash := hongguoSM3([]byte(query))
	counters := bytes.Join([][]byte{
		hongguoPlaybackVarint(nil, 1*8, 1),
		hongguoPlaybackVarint(nil, 2*8, 1),
		hongguoPlaybackVarint(nil, 3*8, 1),
		hongguoPlaybackVarint(nil, 7*8, 0xc792eccc),
	}, nil)
	// 设备子消息：型号串 + 两个数值字段，都是 551 版信封的固定布局。
	device := bytes.Join([][]byte{
		hongguoPlaybackBytesField(nil, 1*8+2, []byte("NX551J")),
		hongguoPlaybackVarint(nil, 2*8, 8196),
		hongguoPlaybackVarint(nil, 4*8, 0x80e0d800),
	}, nil)
	bean := bytes.Join([][]byte{
		hongguoPlaybackVarint(nil, 1*8, 1077940818),
		hongguoPlaybackVarint(nil, 2*8, 2),
		hongguoPlaybackVarint(nil, 3*8, uint64(nonce)),
		hongguoPlaybackBytesField(nil, 4*8+2, []byte(aid)),
		hongguoPlaybackBytesField(nil, 5*8+2, []byte(deviceID)),
		hongguoPlaybackBytesField(nil, 6*8+2, []byte(hongguoPlaybackSdkVersion)),
		hongguoPlaybackBytesField(nil, 7*8+2, []byte(versionName)),
		hongguoPlaybackBytesField(nil, 8*8+2, []byte(hongguoPlaybackProtoVersion)),
		hongguoPlaybackVarint(nil, 9*8, 134744640),
		hongguoPlaybackBytesField(nil, 10*8+2, make([]byte, 8)),
		hongguoPlaybackVarint(nil, 11*8, 0),
		hongguoPlaybackVarint(nil, 12*8, uint64(timestamp)<<1),
		hongguoPlaybackBytesField(nil, 13*8+2, bodyHash[:6]),
		hongguoPlaybackBytesField(nil, 14*8+2, queryHash[:6]),
		hongguoPlaybackBytesField(nil, 15*8+2, counters),
		hongguoPlaybackBytesField(nil, 16*8+2, []byte(installID)),
		hongguoPlaybackBytesField(nil, 20*8+2, []byte("none")),
		hongguoPlaybackVarint(nil, 21*8, 738),
		hongguoPlaybackBytesField(nil, 23*8+2, device),
		hongguoPlaybackVarint(nil, 25*8, 2),
	}, nil)

	plain := hongguoCommentPad(bean)
	hongguoPlaybackSimon(plain)
	mask := []byte{0xf2, 0xf7, 0xfc, 0xff, 0xf2, 0xf7, 0xfc, 0xff}
	tail := []byte{0xea, 0xfb, 0x2c, 0xfe, 0x85, 0x68, 0x51, 0x91, 0x54}
	frame := append(append(append([]byte{}, mask...), plain...), tail...)
	for index := len(mask); index < len(frame); index++ {
		frame[index] ^= mask[index%len(mask)]
	}
	for left, right := 0, len(frame)-1; left < right; left, right = left+1, right-1 {
		frame[left], frame[right] = frame[right], frame[left]
	}
	frame = hongguoCommentPad(append(frame, 0x61, 0x6f))
	block, err := aes.NewCipher(hongguoPlaybackAESKey)
	if err != nil {
		return ""
	}
	cipher.NewCBCEncrypter(block, hongguoPlaybackAESIV).CryptBlocks(frame, frame)
	return base64.StdEncoding.EncodeToString(append([]byte{0xf2, 0x81}, frame...))
}

// hongguoPlaybackLadon 生成 X-Ladon。
//
// 密钥调度：md5(nonce 小端 4 字节 ‖ aid) → hex → 34 轮展开；
// 明文是 "<ts>-<sdkVersion>-<aid>"，用 4 字节 nonce 作前缀。
// 与目录/详情那套 Ladon 的区别只在种子（那边是 nonce[2:6]），块变换本身一致。
func hongguoPlaybackLadon(timestamp, nonce uint32, aid string) string {
	seed := make([]byte, 4)
	binary.LittleEndian.PutUint32(seed, nonce)
	digest := md5.Sum(append(append([]byte{}, seed...), []byte(aid)...))
	hexKey := []byte(hex.EncodeToString(digest[:]))
	var keys [34]uint64
	current := binary.LittleEndian.Uint64(hexKey)
	pending := [3]uint64{
		binary.LittleEndian.Uint64(hexKey[8:]),
		binary.LittleEndian.Uint64(hexKey[16:]),
		binary.LittleEndian.Uint64(hexKey[24:]),
	}
	for index := range keys {
		keys[index] = current
		mixed := (bits.RotateLeft64(pending[0], -8) + current) ^ uint64(index)
		pending = [3]uint64{pending[1], pending[2], mixed}
		current = mixed ^ bits.RotateLeft64(current, 3)
	}
	plain := hongguoCommentPad([]byte(strconv.FormatUint(uint64(timestamp), 10) + "-" + hongguoPlaybackSdkVersion + "-" + aid))
	for offset := 0; offset < len(plain); offset += 16 {
		left, right := binary.LittleEndian.Uint64(plain[offset:]), binary.LittleEndian.Uint64(plain[offset+8:])
		for _, round := range keys {
			right = round ^ (left + bits.RotateLeft64(right, -8))
			left = right ^ bits.RotateLeft64(left, 3)
		}
		binary.LittleEndian.PutUint64(plain[offset:], left)
		binary.LittleEndian.PutUint64(plain[offset+8:], right)
	}
	return base64.StdEncoding.EncodeToString(append(seed, plain...))
}
