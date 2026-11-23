package core

// v5 取流签名的离线回归。期望值是用上游参考实现
// （ZainCheung/smovie internal/hongguo，与 N3urda/hongguoTV 的 Signer.kt 同源）
// 在同一组固定输入下产出的结果，逐字节相同才算移植正确。
//
// 这三条覆盖了整条链路：SM3 + F13 压缩 → xmxor → AesV3 分组加密 → base64 信封。
// 线上表现是「签名错就 HTTP 200 + 0 字节」，没有任何可读报错，
// 所以这层离线比对是唯一能在没有网络时发现回归的手段。

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
)

func TestHongguoV5Constants(t *testing.T) {
	if len(hongguoV5SBox) != 1024 {
		t.Fatalf("v5 sbox 长度 %d，应为 1024", len(hongguoV5SBox))
	}
	if len(hongguoV5Magic) != 16 {
		t.Fatalf("magic 长度 %d，应为 16", len(hongguoV5Magic))
	}
	if len(hongguoV5SignKey) != 32 {
		t.Fatalf("signKey 长度 %d，应为 32", len(hongguoV5SignKey))
	}
	if len(hongguoV5HashTable) != 64 {
		t.Fatalf("F13 常数表长度 %d，应为 64", len(hongguoV5HashTable))
	}
}

func TestHongguoV5SignMatchesReference(t *testing.T) {
	query := "aid=8662&app_name=novelread&_rticket=1794854371289"
	body := []byte("GZIPBODY")
	stamp := uint32(1794854371)
	profile := hongguoV5Profile{
		DeviceID: "333", OSVersion: "12", Model: "PGT-AN10",
		Brand: "HONOR", Channel: "vivo_8662_64", VersionName: "7.4.3.32",
	}
	sequence := uint32(0)
	next := func() (uint32, error) {
		sequence++
		return 0x11111111 * sequence, nil
	}

	if got, want := hongguoV5Helios(stamp, 0x22222222), "IiIiIrRoz0DfX5DG/6AuUc7cddYqLLxRmFQKwRPEFsB1mP+E"; got != want {
		t.Fatalf("X-Helios 不一致\n got=%s\nwant=%s", got, want)
	}

	queryHash := hongguoSM3([]byte(query))
	bodyHash := md5.Sum(body)
	f13 := hex.EncodeToString(hongguoV5F13(queryHash[:], bodyHash[:], stamp))
	if want := "56bd5fe90342a470d5ff64ae487ef4dfbc7f9fa6"; f13 != want {
		t.Fatalf("F13 不一致\n got=%s\nwant=%s", f13, want)
	}

	// 这条期望值是**旧 wire 档**（message field 21 = 377）下的参考产物，
	// 用来锚定算法本身（SM3/F13 → xmxor → AesV3 → 信封），与线上用哪个档位无关。
	// 2026-11-24 起线上改用 312（见 TestHongguoV5MessageField21IsCurrent），
	// 所以这里用 hongguoV5MedusaWithTag 显式传 377 生成旧档产物再比对，
	// 避免把「算法回归」和「档位更新」混为一谈，也不需要改动任何包级状态。
	medusa, err := hongguoV5MedusaWithTag(profile, query, body, stamp, 377,
		"NONCE", int64(1794000000000), next)
	if err != nil {
		t.Fatal(err)
	}
	want := "5k37as4GtKCqOPYp3PhVRsEgNzwiIgABJ8ozOyM3ARYLWJyyCiztk+mNJHzc0luNc4cal6WbkiyADZm2izU+3Jv+o49UtgrGCgRJ0fU/GjUKVuo+aku1oaPsvLNzo7IUugZKHy/qZsPuNgB14CkT1VJe2nZV+tM/QrkcMqlp+V4+/H1vTpXJu1KRB77fAuerXTEP1hkq2z7/PaH1yr/yJOPY/nrTonhPGa7xbcU7iDdXKmPmsLx0KBqgrEcy23aVDPobQi40c3Q30ez3CLRcuz7/lsgq5LlU9+fDvrkDv3UF6ksZAngNeMox7gJSMwrPThKGyc/URexVgdrLa+Gb1Op/PzBBn85WLC3ZaQ34/jdcWXTzY1vPVQChqkhUifZIn0Pl90BBXvCPXuDF3+GpqN1T0pOex/eNyi3EUVDX6JA02UjCbyrNCrGKqiu4/aaiZQ3D6JHKC7aLrS2kwZr/STuNEuSrfi0CixyyodhHr7IkwfN65xUhzjFtw+n1hvZLQh0b6fNf7+wdMj1XGAncKxphdt+IDQRbg2MBUfVpa4WTBcqiWuyJiB12PyvwNzpYbrkbWA3+//7v/v/+7yIi"
	if medusa != want {
		t.Fatalf("X-Medusa 不一致\n got=%s\nwant=%s", medusa, want)
	}
}

// TestHongguoV5MessageField21IsCurrent 钉住线上在用的 medusa 档位。
//
// 2026-11-24 03:36 实测（三个不同 vid，同一登记身份，生产链路）：
//   - field 21 = 377 → 旧 `player/v1` 与 `multi_video_model/v1` 均 HTTP 200 + 0 字节；
//   - field 21 = 312 → 旧 `player/v1` 返回 24.2~24.8KB 的 video_model，
//     总入口拿到 1080P、10 条 variant、CENC 密钥齐全，Range 拉流 206 + video/mp4。
//
// 社区同期参考（N3urda/hongguoTV SIGNING_V5.md 原文）：把「本来失败的 Kotlin Medusa」
// 只改 field 21 为 312 就能出模型，反向把「本来成功的原生 Medusa」改成 377 立刻回到 200/0。
// 所以这个数值不是可调项，改错等于整块功能静默失效，必须有测试兜住。
func TestHongguoV5MessageField21IsCurrent(t *testing.T) {
	if hongguoV5MessageField21 != 312 {
		t.Fatalf("medusa message field 21 = %d，线上可用值是 312（377 会拿到 200+0 字节）",
			hongguoV5MessageField21)
	}
}
