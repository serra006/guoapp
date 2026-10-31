package core

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestHongguoRegisterPacketShape 校验登记封装配方：magic 前缀逐字节一致、
// 密文长度是 16 的整数倍、整体长度 = 6 + 32 + 密文，且相同入参两次打包结果不同
// （salt 随机）。
func TestHongguoRegisterPacketShape(t *testing.T) {
	first, err := hongguoRegisterPacket([]byte("hello hongguo"))
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	second, err := hongguoRegisterPacket([]byte("hello hongguo"))
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	magic := []byte{116, 99, 5, 16, 0, 0}
	if len(first) <= 38 || !bytes.Equal(first[:6], magic) {
		t.Fatalf("magic 头不符: %x", first[:6])
	}
	if (len(first)-38)%16 != 0 {
		t.Fatalf("密文长度不是 16 的整数倍: %d", len(first)-38)
	}
	if bytes.Equal(first, second) {
		t.Fatal("两次打包结果相同，salt 未随机化")
	}
	// PKCS7 填充后明文 = sha512(64B) + data(13B) = 77B，填充到 80B。
	if want := 6 + 32 + 80; len(first) != want {
		t.Fatalf("包长 %d，期望 %d", len(first), want)
	}
}

// TestHongguoRegisterPacketEmptyPayloadStillPadded 空 body 也必须合法填充。
func TestHongguoRegisterPacketEmptyPayloadStillPadded(t *testing.T) {
	packet, err := hongguoRegisterPacket(nil)
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	// sha512 摘要 64B 本身是 16 的整数倍，PKCS7 必须补满一整个块：64 + 16 = 80B。
	if want := 6 + 32 + 80; len(packet) != want {
		t.Fatalf("包长 %d，期望 %d", len(packet), want)
	}
}

// TestHongguoIdentityPersistence 校验身份落盘与读回：写入后能读回同一份，
// 非法/缺字段的内容一律当「没有身份」，文件权限保持 0600 由临时文件继承。
func TestHongguoIdentityPersistence(t *testing.T) {
	directory := t.TempDir()
	d := &Downloader{cfg: Config{dataDir: directory}}
	if state := d.loadHongguoIdentity(); hongguoIdentityValid(state) {
		t.Fatal("空目录不应读到身份")
	}
	state := hongguoIdentityState{DeviceID: "463368267238330", InstallID: "463268267242426", Registered: true}
	if err := d.saveHongguoIdentity(state); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	loaded := d.loadHongguoIdentity()
	if loaded != state {
		t.Fatalf("读回不一致: %+v", loaded)
	}
	// 对外描述只允许出现「已登记/未登记」，绝不能带出身份数值。
	if hongguoIdentityDescription(loaded) != "已登记" || hongguoIdentityDescription(hongguoIdentityState{}) != "未登记" {
		t.Fatal("身份描述不符或泄露了身份数值")
	}
	// 重新构造下载器（模拟重启）也应读回同一份身份。
	restarted := &Downloader{cfg: Config{dataDir: directory}}
	if restarted.loadHongguoIdentity() != state {
		t.Fatal("重启后未读回身份")
	}
	bad := []hongguoIdentityState{
		{DeviceID: "2.29e+15", InstallID: "463268267242426", Registered: true},
		{DeviceID: "463368267238330", InstallID: "", Registered: true},
		{DeviceID: "463368267238330", InstallID: "463268267242426", Registered: false},
		{DeviceID: "0463368267238330", InstallID: "463268267242426", Registered: true},
	}
	for _, candidate := range bad {
		if hongguoIdentityValid(candidate) {
			t.Fatalf("非法身份被接受: %+v", candidate)
		}
	}
	path := filepath.Join(directory, hongguoIdentityFileName)
	if err := os.WriteFile(path, []byte(`{"device_id":"2.29e+15","iid":"463268267242426","registered":true}`), 0o600); err != nil {
		t.Fatalf("写入脏数据失败: %v", err)
	}
	if got := d.loadHongguoIdentity(); hongguoIdentityValid(got) {
		t.Fatalf("脏文件被当作身份: %+v", got)
	}
}

// TestHongguoUngzip 校验 gzip 解压只在魔数匹配时发生。
func TestHongguoUngzip(t *testing.T) {
	if got := hongguoUngzip([]byte("plain")); string(got) != "plain" {
		t.Fatal("非 gzip 内容被改动")
	}
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(`{"device_id":"463368267238330"}`)); err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	if got := string(hongguoUngzip(buffer.Bytes())); got != `{"device_id":"463368267238330"}` {
		t.Fatalf("解压结果不符: %s", got)
	}
	// 截断的 gzip 流应原样返回，不能返回空或 panic。
	if got := hongguoUngzip(buffer.Bytes()[:8]); len(got) == 0 {
		t.Fatal("损坏的 gzip 流返回空")
	}
}

// TestHongguoPlaybackNeedsNewIdentity 校验「换身份重试」的触发条件：
// 只有空响应与「响应里没有该 vid」才重试，其它错误不重试。
func TestHongguoPlaybackNeedsNewIdentity(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errHongguoAppEmpty, true},
		{errHongguoAppNoMedia, true},
		{errors.New("其它错误"), false},
	}
	for _, item := range cases {
		if got := hongguoPlaybackNeedsNewIdentity(item.err); got != item.want {
			t.Fatalf("%v: got %v want %v", item.err, got, item.want)
		}
	}
}
