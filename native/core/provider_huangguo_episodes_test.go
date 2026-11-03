package core

import "testing"

// 这类站点详情页里，分集列表和「推荐位」用的都是 /video/... 链接，长得一模一样，
// 只有分集列表在 data-web-play-ep-grid 容器里。曾经按全页正则扫，结果每部剧的
// 前几集都指向同一批推荐视频（实测两部不同剧的 ep1 解析出同一个 m3u8），
// 用户看到的就是「放的不是这部剧」。这个用例把容器边界钉住。
func TestParseHuangguoAIEpisodesUsesGridOnly(t *testing.T) {
	const page = `
<div class="hg-drama-card"><a href="/video/9001/">推荐片 A</a></div>
<div class="hg-drama-card"><a href="/video/9002/">推荐片 B</a></div>
<div class="hg-web-play__episode-scroll" data-web-play-episode-scroll>
  <div class="hg-web-play__ep-grid" data-web-play-ep-grid>
    <a class="hg-web-play__ep is-active" href="/video/12/" data-ep-id="1">01</a>
    <a class="hg-web-play__ep" href="/video/12/ep-2/" data-ep-id="2">02</a>
    <a class="hg-web-play__ep" href="/video/12/ep-3/" data-ep-id="3">03</a>
  </div>
</div>
<div class="hg-drama-card"><a href="/video/9003/">推荐片 C</a></div>`

	episodes := parseHuangguoAIEpisodes(page, "https://huangguoai.com/detail/12/", "12")
	if len(episodes) != 3 {
		t.Fatalf("分集数应为 3，实际 %d（%+v）", len(episodes), episodes)
	}
	want := []string{
		"https://huangguoai.com/video/12/",
		"https://huangguoai.com/video/12/ep-2/",
		"https://huangguoai.com/video/12/ep-3/",
	}
	for index, episode := range episodes {
		if episode.URL != want[index] {
			t.Errorf("第 %d 集地址 = %q，期望 %q", index+1, episode.URL, want[index])
		}
		if episode.Index != index+1 {
			t.Errorf("第 %d 集集号 = %d", index+1, episode.Index)
		}
		if episode.Title != "第"+itoaSmall(index+1)+"集" {
			t.Errorf("第 %d 集标题 = %q", index+1, episode.Title)
		}
	}
}

// 没有分集容器时（单集视频页）要退回旧的全页扫描，不能把功能做没。
func TestParseHuangguoAIEpisodesFallsBackWithoutGrid(t *testing.T) {
	const page = `<div class="video-wrap"><a href="/video/777/" title="第1集">第1集</a></div>`
	episodes := parseHuangguoAIEpisodes(page, "https://huangguoai.com/detail/777/", "777")
	if len(episodes) != 1 {
		t.Fatalf("兜底路径应解析出 1 集，实际 %d", len(episodes))
	}
	if episodes[0].URL != "https://huangguoai.com/video/777/" {
		t.Fatalf("兜底路径地址 = %q", episodes[0].URL)
	}
}

func itoaSmall(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
