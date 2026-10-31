package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
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
	"time"
)

// 红果（com.phoenix.read）自 2026-11 起不再接受本地随机生成的设备身份：
// 用 newHongguoDeviceID() 造出来的 device_id/iid 去打播放接口，服务端一律返回
// HTTP 200 + 0 字节。必须先在官方设备登记接口 POST /service/2/device_register/
// 拿到服务端下发的 device_id/iid，再用这套身份取流（且不能带三签名）。
const (
	hongguoRegisterEndpoint  = "https://log.snssdk.com/service/2/device_register/"
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
}

// hongguoIdentityCall 是一次进行中的登记请求。并发播放共用同一份结果，
// 避免「每个播放失败都各自去登记」把风控打满。
type hongguoIdentityCall struct {
	done      chan struct{}
	deviceID  string
	installID string
	err       error
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

// registerHongguoDevice 走官方设备登记接口领取一套 device_id/iid。
// 该请求同样不签名（登记接口不接受本地签名），复用下载器的限流器、传输层与 20 秒超时。
func (downloader *Downloader) registerHongguoDevice(ctx context.Context) (hongguoIdentityState, error) {
	fingerprint := hongguoRegisterDeviceFP()
	header := map[string]any{
		"display_name": "红果免费短剧", "aid": 8662, "package": "com.phoenix.read",
		"app_version": hongguoRegisterVersionNa, "version_code": hongguoRegisterVersion,
		"manifest_version_code": hongguoRegisterVersion, "update_version_code": hongguoRegisterVersion,
		"channel": "update_64", "sdk_version": "3.7.3", "sdk_target_version": 36,
		"os": "Android", "os_version": "16", "os_api": 36,
		"device_model": "25053RT47C", "device_brand": "Redmi", "device_manufacturer": "Xiaomi",
		"device_category": "phone", "cpu_abi": "arm64-v8a", "density_dpi": 520,
		"display_density": "mdpi", "resolution": "1280x2772", "language": "zh",
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		hongguoRegisterEndpoint+"?"+params.Encode(), bytes.NewReader(body))
	if err != nil {
		return hongguoIdentityState{}, errors.New("无法准备红果设备登记请求")
	}
	request.Header.Set("Content-Type", "application/octet-stream;tt-data=a")
	request.Header.Set("User-Agent", hongguoRegisterUserAgent)
	// 自己声明 gzip 才能拿到压缩体：Go 的传输层只在「没手动设置」时才自动解压。
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := downloader.doPreparedCatalogRequest(request, 20*time.Second, nil)
	if err != nil {
		return hongguoIdentityState{}, fmt.Errorf("红果设备登记失败: %w", publicError(err))
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, hongguoRegisterMaxBody))
	response.Body.Close()
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
		DeviceID:   mapString(result, "device_id"),
		InstallID:  mapString(result, "install_id", "iid"),
		Registered: true,
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

// hongguoPlaybackIdentity 返回取流要用的登记身份。
// refresh 为 false 时优先用内存/磁盘里已有的身份；refresh 为 true 时强制重新登记
// （用于「空响应 → 换身份重试一次」）。并发调用共用同一次登记请求。
func (downloader *Downloader) hongguoPlaybackIdentity(ctx context.Context, refresh bool) (string, string, error) {
	client := downloader.hongguoClient()
	client.identityMu.Lock()
	if !client.identityLoaded {
		client.identity = downloader.loadHongguoIdentity()
		client.identityLoaded = true
	}
	if !refresh && hongguoIdentityValid(client.identity) {
		deviceID, installID := client.identity.DeviceID, client.identity.InstallID
		client.identityMu.Unlock()
		return deviceID, installID, nil
	}
	if pending := client.identityCall; pending != nil {
		client.identityMu.Unlock()
		select {
		case <-pending.done:
			return pending.deviceID, pending.installID, pending.err
		case <-ctx.Done():
			return "", "", ctx.Err()
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
		call.deviceID, call.installID = state.DeviceID, state.InstallID
	}
	client.identityCall = nil
	client.identityMu.Unlock()
	close(call.done)
	if err != nil {
		return "", "", err
	}
	// 落盘失败不影响本次播放（身份已在内存里），下次播放会再登记一次。
	_ = downloader.saveHongguoIdentity(state)
	return call.deviceID, call.installID, nil
}

// hongguoPlaybackIdentityOrFallback 在登记不可用时回落到内存里的随机身份，
// 让旧链路（preload / video_model 等）仍能按原有行为尝试一次。
func (downloader *Downloader) hongguoPlaybackIdentityOrFallback(ctx context.Context, refresh bool) (string, string) {
	deviceID, installID, err := downloader.hongguoPlaybackIdentity(ctx, refresh)
	if err == nil && deviceID != "" && installID != "" {
		return deviceID, installID
	}
	client := downloader.hongguoClient()
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.state.DeviceID, client.state.InstallID
}

// hongguoIdentityDescription 给诊断用的「是否已登记」描述，绝不带出真实身份数值。
func hongguoIdentityDescription(state hongguoIdentityState) string {
	if hongguoIdentityValid(state) {
		return "已登记"
	}
	return "未登记"
}
