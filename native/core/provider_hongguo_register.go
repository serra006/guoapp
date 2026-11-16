package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 红果（com.phoenix.read）自 2026-11 起不再接受本地随机生成的设备身份：
// 用 newHongguoDeviceID() 造出来的 device_id/iid 去打播放接口，服务端一律返回
// HTTP 200 + 0 字节。必须先在官方设备登记接口 POST /service/2/device_register/
// 拿到服务端下发的 device_id/iid，再用这套身份取流（且不能带三签名）。
// hongguoRegisterEndpoint 是设备登记入口。做成变量而不是常量，是为了让
// 排查脚本可以临时改写它去对照不同登记集群（lf/lq/snssdk）下发的身份。
var hongguoRegisterEndpoint = "https://log.snssdk.com/service/2/device_register/"

const (
	hongguoRegisterVersion   = "73532"
	hongguoRegisterVersionNa = "7.3.5.32"
	// 登记 body 的自定义加密封装配方（与设备登记探针逐字节一致，勿改动）。
	hongguoRegisterSeedHex = "4dd4c2e6b83162090e52b3c7a6733ba41cb2462b829ab58a196b39db57177524f49baf7f08e8d68d26a72e37c1a95a2f1f05a51892aef2949732b62a38aadd58"
	// 登记请求与取流请求用的是两条不同的 UA：登记只带机型，取流带 Cronet 版本尾巴。
	hongguoRegisterUserAgent = "com.phoenix.read/73532 (Linux; U; Android 16; zh_CN; 25053RT47C)"
	// 身份状态文件名，与 dataDir 下其它本地状态文件（huangguo-session.json、
	// rankings.json）同级同风格。
	hongguoIdentityFileName = "hongguo-identity.json"
	// 登记响应体的上限。正常响应 < 2KB，给足余量但不做无限读。
	hongguoRegisterMaxBody = 64 * 1024
)

// hongguoRegisteredIDPattern 校验服务端下发的身份：15~20 位纯数字且首位非 0。
// 注意 device_id 落在 int64 之外是常态（19~20 位），所以解析必须用 json.Number，
// 用 float64 会退化成 2.29e+15 这种科学计数法，精度全丢。
var hongguoRegisteredIDPattern = regexp.MustCompile(`^[1-9][0-9]{14,19}$`)

// hongguoIdentityState 是设备登记身份的持久化形态。
type hongguoIdentityState struct {
	DeviceID   string `json:"device_id"`
	InstallID  string `json:"iid"`
	Registered bool   `json:"registered"`
	// 登记时提交的设备指纹。取流请求的 query 必须复用同一套值——服务端会把
	// query 里的 cdid/openudid 与登记记录比对，换值等于换设备。
	CDID       string `json:"cdid,omitempty"`
	OpenUDID   string `json:"openudid,omitempty"`
	ClientUDID string `json:"clientudid,omitempty"`
	// 登记响应下发的游客票据（Set-Cookie）。取流要走 guestCookie 把它带回服务端，
	// 只有 device_id/iid 而没有 ttreq 时，播放接口返回 HTTP 200 + 0 字节。
	TTReq          string `json:"ttreq,omitempty"`
	StoreRegion    string `json:"store_region,omitempty"`
	StoreRegionSrc string `json:"store_region_src,omitempty"`
	// 登记时随机选定的机型。取流必须复用同一台，否则服务端把 query 里的机型
	// 与登记记录一对就判为异常。
	DeviceModel        string `json:"device_model,omitempty"`
	DeviceBrand        string `json:"device_brand,omitempty"`
	DeviceManufacturer string `json:"device_manufacturer,omitempty"`
	DeviceOSVersion    string `json:"device_os_version,omitempty"`
	DeviceOSAPI        string `json:"device_os_api,omitempty"`
	DeviceBuild        string `json:"device_build,omitempty"`
	DeviceResolution   string `json:"device_resolution,omitempty"`
	DeviceDPI          string `json:"device_dpi,omitempty"`
	DeviceChannel      string `json:"device_channel,omitempty"`
}

// device 还原身份里保存的机型；老身份文件没有这些字段时回落到预设第一台。
func (state hongguoIdentityState) device() hongguoDeviceProfile {
	if state.DeviceModel == "" {
		return hongguoDevicePresets[0]
	}
	return hongguoDeviceProfile{
		Model: state.DeviceModel, Brand: state.DeviceBrand, Manufacturer: state.DeviceManufacturer,
		OSVersion: state.DeviceOSVersion, OSAPI: state.DeviceOSAPI, Build: state.DeviceBuild,
		Resolution: state.DeviceResolution, DPI: state.DeviceDPI, Channel: state.DeviceChannel,
	}
}

// hongguoIdentityCall 是一次进行中的登记请求。并发播放共用同一份结果，
// 避免「每个播放失败都各自去登记」把风控打满。
type hongguoIdentityCall struct {
	done  chan struct{}
	state hongguoIdentityState
	err   error
}

// hongguoIdentityValid 判断身份是否可用于取流。
func hongguoIdentityValid(state hongguoIdentityState) bool {
	return state.Registered &&
		hongguoRegisteredIDPattern.MatchString(state.DeviceID) &&
		hongguoRegisteredIDPattern.MatchString(state.InstallID)
}

// hongguoRegisterPacket 按红果设备登记的私有封装配方打包 body：
//
//	magic [116,99,5,16,0,0] + salt(32B 随机)
//	+ AES-128-CBC(sha512(data) || data, key=material[0:16], iv=material[16:32])
//
// 其中 material = sha512(sha512(salt) || SEED)，PKCS7 填充。配方与线上 APK
// 一致，任一字节的偏差都会被服务端判为非法包（表现为 HTTP 200 + 空体）。
func hongguoRegisterPacket(data []byte) ([]byte, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(hongguoRegisterSeedHex)
	if err != nil {
		return nil, err
	}
	saltHash := sha512.Sum512(salt)
	mix := append(append([]byte{}, saltHash[:]...), seed...)
	material := sha512.Sum512(mix)
	block, err := aes.NewCipher(material[:16])
	if err != nil {
		return nil, err
	}
	bodyHash := sha512.Sum512(data)
	plain := append(append([]byte{}, bodyHash[:]...), data...)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, material[16:32]).CryptBlocks(encrypted, plain)
	packet := append([]byte{116, 99, 5, 16, 0, 0}, salt...)
	return append(packet, encrypted...), nil
}

// hongguoRegisterDeviceID 生成一次登记的随机设备指纹集合。这些值只在登记时用一次，
// 不作为身份持久化（真正的身份是服务端下发的 device_id/iid）。
func hongguoRegisterDeviceFP() map[string]string {
	return map[string]string{
		"openudid":   fmt.Sprintf("%016x", time.Now().UnixNano()),
		"clientudid": uuidLike(),
		"cdid":       uuidLike(),
	}
}

// hongguoDeviceProfile 是一次登记与随后取流共用的一台设备指纹。
//
// 为什么要随机而不是写死一台：服务端会把取流 query 里的机型信息与登记记录比对，
// 同时对「开源项目里反复出现的那几台固定机型」做静默拉黑 —— 表现就是 HTTP 200 +
// 0 字节空体，从外面看与接口整体挂掉完全一样。所以机型一次随机选定，写进身份状态，
// 之后登记、取流、UA 全部复用它。
type hongguoDeviceProfile struct {
	Model        string
	Brand        string
	Manufacturer string
	OSVersion    string
	OSAPI        string
	Build        string
	// Resolution 用 register 端的 "1080x2400" 写法，取流时再换成 "1080*2400"。
	Resolution string
	DPI        string
	Channel    string
}

// hongguoDevicePresets 都是真实在售机型的公开参数（型号 / 厂商 / 分辨率 / DPI 自洽）。
var hongguoDevicePresets = []hongguoDeviceProfile{
	{"22021211RC", "Xiaomi", "Xiaomi", "13", "33", "TKQ1.221114.001", "1080x2400", "440", "update_64"},
	{"V2227A", "vivo", "vivo", "13", "33", "TP1A.220624.014", "1260x2800", "480", "update_64"},
	{"CMA-AN00", "HONOR", "HONOR", "12", "31", "SP1A.210812.001", "1080x2340", "400", "update_64"},
	{"PGBM10", "OPPO", "OPPO", "14", "34", "UKQ1.230924.001", "1240x2772", "450", "update_64"},
	{"RMX3706", "realme", "realme", "13", "33", "TP1A.220905.001", "1080x2412", "400", "update_64"},
	{"SM-S9110", "samsung", "samsung", "13", "33", "TP1A.220624.014", "1080x2340", "420", "update_64"},
}

// pickHongguoDeviceProfile 随机挑一台，避免所有安装共用同一台机。
func pickHongguoDeviceProfile() hongguoDeviceProfile {
	var buffer [2]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return hongguoDevicePresets[0]
	}
	index := int(buffer[0])<<8 | int(buffer[1])
	return hongguoDevicePresets[index%len(hongguoDevicePresets)]
}

// queryResolution 把 register 端的 "1080x2400" 换成取流 query 的 "1080*2400"。
func (profile hongguoDeviceProfile) queryResolution() string {
	return strings.ReplaceAll(profile.Resolution, "x", "*")
}

// userAgent 拼出该机型的 Cronet UA。版本尾巴与官方客户端一致，机型必须与 query 相同。
func (profile hongguoDeviceProfile) userAgent() string {
	return fmt.Sprintf("com.phoenix.read/%s (Linux; U; Android %s; zh_CN; %s; Build/%s; Cronet/TTNetVersion:8d40f833 2026-03-03 QuicVersion:21ac1950 2025-11-18)",
		hongguoRegisterVersion, profile.OSVersion, profile.Model, profile.Build)
}

// registerHongguoDevice 走官方设备登记接口领取一套 device_id/iid。
// 该请求同样不签名（登记接口不接受本地签名），复用下载器的限流器、传输层与 20 秒超时。
func (downloader *Downloader) registerHongguoDevice(ctx context.Context) (hongguoIdentityState, error) {
	fingerprint := hongguoRegisterDeviceFP()
	reqID := uuidLike()
	device := pickHongguoDeviceProfile()
	header := map[string]any{
		"display_name": "红果免费短剧", "aid": 8662, "package": "com.phoenix.read",
		"app_version": hongguoRegisterVersionNa, "version_code": hongguoRegisterVersion,
		"manifest_version_code": hongguoRegisterVersion, "update_version_code": hongguoRegisterVersion,
		"channel": device.Channel, "sdk_version": "3.7.3", "sdk_target_version": 36,
		"os": "Android", "os_version": device.OSVersion, "os_api": device.OSAPI,
		"device_model": device.Model, "device_brand": device.Brand, "device_manufacturer": device.Manufacturer,
		"device_category": "phone", "cpu_abi": "arm64-v8a", "density_dpi": device.DPI,
		"display_density": "mdpi", "resolution": device.Resolution, "language": "zh",
		"timezone": 8, "access": "wifi", "region": "CN", "tz_name": "Asia/Shanghai",
		"tz_offset": 28800, "openudid": fingerprint["openudid"],
		"clientudid": fingerprint["clientudid"], "cdid": fingerprint["cdid"],
	}
	payload, err := json.Marshal(map[string]any{
		"magic_tag": "ss_app_log", "header": header, "_gen_time": time.Now().UnixMilli(),
	})
	if err != nil {
		return hongguoIdentityState{}, errors.New("无法准备红果设备登记请求")
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return hongguoIdentityState{}, errors.New("无法准备红果设备登记请求")
	}
	if err := writer.Close(); err != nil {
		return hongguoIdentityState{}, errors.New("无法准备红果设备登记请求")
	}
	body, err := hongguoRegisterPacket(compressed.Bytes())
	if err != nil {
		return hongguoIdentityState{}, errors.New("无法准备红果设备登记请求")
	}
	params := url.Values{
		"aid": {"8662"}, "package": {"com.phoenix.read"}, "version_code": {hongguoRegisterVersion},
		"manifest_version_code": {hongguoRegisterVersion}, "update_version_code": {hongguoRegisterVersion},
		"channel": {"update_64"}, "os_version": {"16"}, "os_api": {"36"},
		"device_brand": {"Redmi"}, "language": {"zh"}, "app_name": {"novelread"},
		"version_name": {hongguoRegisterVersionNa}, "device_type": {"25053RT47C"},
		"device_platform": {"android"}, "os": {"android"}, "tt_data": {"a"},
		"ssmix": {"a"}, "_rticket": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
		"ts":       {strconv.FormatInt(time.Now().Unix(), 10)},
		"openudid": {fingerprint["openudid"]}, "clientudid": {fingerprint["clientudid"]},
		"cdid": {fingerprint["cdid"]},
	}
	params.Set("req_id", reqID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		hongguoRegisterEndpoint+"?"+params.Encode(), bytes.NewReader(body))
	if err != nil {
		return hongguoIdentityState{}, errors.New("无法准备红果设备登记请求")
	}
	request.Header.Set("Content-Type", "application/octet-stream;tt-data=a")
	request.Header.Set("User-Agent", hongguoRegisterUserAgent)
	// 自己声明 gzip 才能拿到压缩体：Go 的传输层只在「没手动设置」时才自动解压。
	request.Header.Set("Accept-Encoding", "gzip")
	// 下面这组头是官方客户端的登记标配。缺 log-encode-type 时服务端按未压缩解析，
	// 缺 x-ss-dp 时不认 app 归属，两者都会让登记"成功"但拿不到游客票据。
	request.Header.Set("log-encode-type", "gzip")
	request.Header.Set("sdk-version", "2")
	request.Header.Set("passport-sdk-version", "5051452")
	request.Header.Set("x-vc-bdturing-sdk-version", "4.0.3.cn")
	request.Header.Set("X-SS-DP", "8662")
	request.Header.Set("X-SS-STUB", fmt.Sprintf("%X", md5.Sum(body)))
	response, err := downloader.doPreparedCatalogRequest(request, 20*time.Second, nil)
	if err != nil {
		return hongguoIdentityState{}, fmt.Errorf("红果设备登记失败: %w", publicError(err))
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, hongguoRegisterMaxBody))
	response.Body.Close()
	cookies := map[string]string{}
	for _, cookie := range response.Cookies() {
		cookies[cookie.Name] = cookie.Value
	}
	if readErr != nil {
		return hongguoIdentityState{}, errors.New("红果设备登记失败：响应读取中断")
	}
	if response.StatusCode != http.StatusOK {
		return hongguoIdentityState{}, fmt.Errorf("红果设备登记失败 HTTP %d", response.StatusCode)
	}
	raw = hongguoUngzip(raw)
	if len(raw) == 0 {
		return hongguoIdentityState{}, errors.New("红果设备登记未返回有效数据")
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || result == nil {
		return hongguoIdentityState{}, errors.New("红果设备登记响应格式异常")
	}
	state := hongguoIdentityState{
		DeviceID:           mapString(result, "device_id"),
		InstallID:          mapString(result, "install_id", "iid"),
		Registered:         true,
		CDID:               fingerprint["cdid"],
		OpenUDID:           fingerprint["openudid"],
		ClientUDID:         fingerprint["clientudid"],
		TTReq:              cookies["ttreq"],
		StoreRegion:        cookies["store-region"],
		StoreRegionSrc:     cookies["store-region-src"],
		DeviceModel:        device.Model,
		DeviceBrand:        device.Brand,
		DeviceManufacturer: device.Manufacturer,
		DeviceOSVersion:    device.OSVersion,
		DeviceOSAPI:        device.OSAPI,
		DeviceBuild:        device.Build,
		DeviceResolution:   device.Resolution,
		DeviceDPI:          device.DPI,
		DeviceChannel:      device.Channel,
	}
	if !hongguoIdentityValid(state) {
		return hongguoIdentityState{}, errors.New("红果设备登记未返回可用身份")
	}
	return state, nil
}

// hongguoUngzip 在响应体带 gzip 魔数时解压，否则原样返回。
// 登记接口默认压缩响应，不解压会直接解析失败。
func hongguoUngzip(raw []byte) []byte {
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	defer reader.Close()
	plain, err := io.ReadAll(io.LimitReader(reader, hongguoRegisterMaxBody*16))
	if err != nil || len(plain) == 0 {
		return raw
	}
	return plain
}

// loadHongguoIdentity 读取持久化的登记身份。任何异常都当作「没有身份」处理，
// 调用方回落到内存里的随机身份（即旧行为），随后再走登记修复。
func (downloader *Downloader) loadHongguoIdentity() hongguoIdentityState {
	directory := downloader.cfg.dataDir
	if directory == "" {
		return hongguoIdentityState{}
	}
	file, err := os.Open(filepath.Join(directory, hongguoIdentityFileName))
	if err != nil {
		return hongguoIdentityState{}
	}
	defer file.Close()
	var state hongguoIdentityState
	if json.NewDecoder(io.LimitReader(file, 8*1024)).Decode(&state) != nil {
		return hongguoIdentityState{}
	}
	if !hongguoIdentityValid(state) {
		return hongguoIdentityState{}
	}
	return state
}

// saveHongguoIdentity 原子写入身份状态（临时文件 + rename，权限 0700/0600），
// 与 huangguo-session.json 的落盘方式保持一致。device_id/iid 属于用户隐私，
// 只落盘、绝不写日志。
func (downloader *Downloader) saveHongguoIdentity(state hongguoIdentityState) error {
	directory := downloader.cfg.dataDir
	if directory == "" || !hongguoIdentityValid(state) {
		return errors.New("红果身份状态不可保存")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".hongguo-identity-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(state); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, hongguoIdentityFileName))
}

// hongguoPlaybackIdentityState 返回取流要用的登记身份（完整状态）。
//
// 为什么必须带状态而不是只带 device_id/iid：取流请求要复现登记时那台机（机型、
// 分辨率、DPI、渠道）、那两个设备指纹（cdid / openudid）和登记下发的游客票据
// （ttreq）。只传两个 ID 的话，调用方只能把机型写死，而写死的那几台机早被静默
// 拉黑（表现仍是 HTTP 200 + 0 字节）。
//
// refresh 为 false 时优先用内存/磁盘里已有的身份；refresh 为 true 时强制重新登记
// （用于「空响应 → 换身份重试一次」）。并发调用共用同一次登记请求。
func (downloader *Downloader) hongguoPlaybackIdentityState(ctx context.Context, refresh bool) (hongguoIdentityState, error) {
	client := downloader.hongguoClient()
	client.identityMu.Lock()
	if !client.identityLoaded {
		client.identity = downloader.loadHongguoIdentity()
		client.identityLoaded = true
	}
	if !refresh && hongguoIdentityValid(client.identity) {
		current := client.identity
		client.identityMu.Unlock()
		return current, nil
	}
	if pending := client.identityCall; pending != nil {
		client.identityMu.Unlock()
		select {
		case <-pending.done:
			return pending.state, pending.err
		case <-ctx.Done():
			return hongguoIdentityState{}, ctx.Err()
		}
	}
	call := &hongguoIdentityCall{done: make(chan struct{})}
	client.identityCall = call
	client.identityMu.Unlock()

	state, err := downloader.registerHongguoDevice(ctx)

	client.identityMu.Lock()
	call.err = err
	if err == nil {
		client.identity = state
		client.identityLoaded = true
		call.state = state
	}
	client.identityCall = nil
	client.identityMu.Unlock()
	close(call.done)
	if err != nil {
		return hongguoIdentityState{}, err
	}
	// 落盘失败不影响本次播放（身份已在内存里），下次播放会再登记一次。
	_ = downloader.saveHongguoIdentity(state)
	return state, nil
}

// hongguoPlaybackIdentityOrFallback 在登记不可用时回落到内存里的随机身份，
// 让旧链路（preload / multi_video_model 等）仍能按原有行为尝试一次。
func (downloader *Downloader) hongguoPlaybackIdentityOrFallback(ctx context.Context, refresh bool) hongguoIdentityState {
	state, err := downloader.hongguoPlaybackIdentityState(ctx, refresh)
	if err == nil && hongguoIdentityValid(state) {
		return state
	}
	client := downloader.hongguoClient()
	client.mu.Lock()
	defer client.mu.Unlock()
	return hongguoIdentityState{DeviceID: client.state.DeviceID, InstallID: client.state.InstallID}
}

// hongguoGuestCookie 把登记下发的游客票据拼成 Cookie 头。
//
// 顺序与空值形态跟官方客户端一致：passport_csrf_token 那两条留空但**必须存在**，
// 少了会被当成另一类客户端。ttreq 为空时调用方不该带 Cookie 头（见
// hongguoAppPlaybackRequest），而不是带一个空 ttreq。
func hongguoGuestCookie(state hongguoIdentityState) string {
	return "passport_csrf_token=; passport_csrf_token_default=; store-region=" + state.StoreRegion +
		"; store-region-src=" + state.StoreRegionSrc + "; install_id=" + state.InstallID + "; ttreq=" + state.TTReq
}

// hongguoIdentityDescription 给诊断用的「是否已登记」描述，绝不带出真实身份数值。
func hongguoIdentityDescription(state hongguoIdentityState) string {
	if hongguoIdentityValid(state) {
		return "已登记"
	}
	return "未登记"
}
