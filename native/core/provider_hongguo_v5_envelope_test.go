package core

// 红果 v5 完整 device 信封的生产回归测试：本地离线，不联网、不设 HG_LIVE。
//
// 钉住四件事：74532 profile 的身份三件套来源、25 字符 device token 的补齐/截断规则、
// 固定随机源下签名的逐字节确定性，以及空响应冷却闸门的开合。签名本身不断言具体值
// （含时间戳与随机盐），只断言同输入两次完全一致，避免把算法漂移误判成通过。

import (
	"errors"
	"strings"
	"testing"
)

// TestHongguoV5EnvelopeProfilePinned 确认完整信封用的是 74532 / 7.4.5.32，
// 但身份三件套仍来自传入的 state（不是写死的设备）。
func TestHongguoV5EnvelopeProfilePinned(t *testing.T) {
	state := hongguoV5TestState()
	profile := hongguoV5EnvelopeProfile(state)
	if profile.VersionCode != "74532" {
		t.Fatalf("VersionCode=%q，应为 74532", profile.VersionCode)
	}
	if profile.VersionName != "7.4.5.32" {
		t.Fatalf("VersionName=%q，应为 7.4.5.32", profile.VersionName)
	}
	for field, got := range map[string]string{
		"DeviceID":  profile.DeviceID,
		"InstallID": profile.InstallID,
		"CDID":      profile.CDID,
	} {
		want := state.DeviceID
		switch field {
		case "InstallID":
			want = state.InstallID
		case "CDID":
			want = state.CDID
		}
		if got != want {
			t.Fatalf("信封 profile %s=%q，应来自 state 的 %q", field, got, want)
		}
	}
}

// TestHongguoV5EnvelopeDeviceToken 钉住 'A' + 24 字符的规则：
// 短 nonce 尾部补 'A'，长 nonce 直接截断到 24 个字符。
func TestHongguoV5EnvelopeDeviceToken(t *testing.T) {
	cases := []struct {
		name  string
		nonce string
		want  string
	}{
		{"短 nonce 补 A", "SHORT", "A" + "SHORT" + strings.Repeat("A", 19)},
		{"长 nonce 截断", strings.Repeat("Z", 30), "A" + strings.Repeat("Z", 24)},
		{"恰好 24 字符", "abcdefghijklmnopqrstuvwx", "Aabcdefghijklmnopqrstuvwx"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			token := hongguoV5EnvelopeDeviceToken(testCase.nonce)
			if len(token) != 25 {
				t.Fatalf("token 长度 %d，应为 25: %q", len(token), token)
			}
			if token[0] != 'A' {
				t.Fatalf("token 首字符是 %q，应为 'A'", token[0])
			}
			if token != testCase.want {
				t.Fatalf("token=%q，应为 %q", token, testCase.want)
			}
		})
	}
}

// TestHongguoV5EnvelopeMedusaDeterministic 在固定时钟与固定随机源下调用两次签名，
// 断言逐字节一致。不比对任何 golden 串：时间戳/盐会变，只有确定性是可钉的。
func TestHongguoV5EnvelopeMedusaDeterministic(t *testing.T) {
	originalClock := hongguoV5EnvelopeNowMillis
	hongguoV5EnvelopeNowMillis = func() int64 { return 1764550000123 }
	defer func() { hongguoV5EnvelopeNowMillis = originalClock }()

	profile := hongguoV5EnvelopeProfile(hongguoV5TestState())
	const query = "device_platform=android&os=android&ssmix=a&_rticket=1764550000123&device_id=7702409114256690238"
	body := []byte{0x1f, 0x8b, 0x08, 0x00, 0xd3, 0x2a, 0x1c, 0x67, 0x00, 0x03}
	const stamp uint32 = 1764550000
	const nonce = "abcdefghijklmnopqrstuvwx"
	const started int64 = 1764550000000

	const fixedRandom uint32 = 0x12345678
	used := 0
	// 每次调用都返回同一个固定值，保证两次调用的注入输入逐字节相同。
	next := func() (uint32, error) {
		used++
		return fixedRandom, nil
	}

	first, err := hongguoV5EnvelopeMedusaWith(profile, query, body, stamp, nonce, started, next)
	if err != nil {
		t.Fatalf("第一次签名失败: %v", err)
	}
	if first == "" {
		t.Fatal("签名结果为空")
	}
	second, err := hongguoV5EnvelopeMedusaWith(profile, query, body, stamp, nonce, started, next)
	if err != nil {
		t.Fatalf("第二次签名失败: %v", err)
	}
	if first != second {
		t.Fatalf("同输入两次签名不一致:\n first  %q\n second %q", first, second)
	}
	if used != 10 {
		t.Fatalf("随机源抽取次数 %d，应为 10（每次签名 5 次）", used)
	}

	// 随机源报错必须原样冒出去，不能静默产出半截签名。
	broken := func() (uint32, error) { return 0, errors.New("随机源故障") }
	if _, err := hongguoV5EnvelopeMedusaWith(profile, query, body, stamp, nonce, started, broken); err == nil {
		t.Fatal("随机源报错时签名没有返回错误")
	}
}

// TestHongguoV5EmptyCooldownGate 钉住空响应冷却闸门：清零 → 关闭 → 再清零。
func TestHongguoV5EmptyCooldownGate(t *testing.T) {
	defer hongguoV5ClearEmptyCooldown()

	hongguoV5ClearEmptyCooldown()
	if hongguoV5EmptyCooling() {
		t.Fatal("清零后仍处在空响应冷却中")
	}
	hongguoV5MarkEmpty()
	if !hongguoV5EmptyCooling() {
		t.Fatal("MarkEmpty 后没有进入冷却")
	}
	hongguoV5ClearEmptyCooldown()
	if hongguoV5EmptyCooling() {
		t.Fatal("二次清零后仍处在空响应冷却中")
	}
}
