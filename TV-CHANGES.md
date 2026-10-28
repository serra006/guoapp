# Android TV 适配改造说明

基线：上游 guoapp 0.2.64+71（zip 快照，无 git）。本次改造目标：独立 TV 版安卓应用「真果TV」（`com.duanju.zhenguo.tv`），修复全部 P0 级电视不可用问题。

## 一、独立 TV 版（flavor `tv`）

| 项 | 手机版（flavor `phone`） | 电视版（flavor `tv`） |
| --- | --- | --- |
| 包名 | `com.duanju.zhenguojian` | `com.duanju.zhenguo.tv` |
| 应用名 | 真果鉴 | 真果TV |
| versionName | 0.2.64 | 0.2.64-tv |
| 桌面入口 | 手机桌面 | 仅电视桌面（LEANBACK_LAUNCHER） |
| 数据 | 独立 | 独立（可两版并存） |

改动文件：

- `android/app/build.gradle.kts`：新增 `flavorDimensions platform` + `phone`/`tv` flavor。**引入 flavor 后必须始终 `--flavor` 构建**，裸 `flutter build apk` 会因 Gradle task 缺失失败。
- `android/app/src/tv/AndroidManifest.xml`：用 `tools:node="replace"` 替换 MAIN intent-filter，只保留 LEANBACK_LAUNCHER；banner/label 走 manifestPlaceholders。
- `lib/app_build.dart`：新增 `TV_BUILD` dart-define，`appName` 在电视端固定为「真果TV」。
- `scripts/app_build.py`：`BuildVariant` 增加 `flavor` 字段；`flutter_arguments` 始终携带 `--flavor`；tv 额外注入 `--dart-define=TV_BUILD=true`，并强制 `all_sources=True`（电视端只发全站源单版本）；产物 slug 为 `zhenguojian-tv-x.y.z`。
- `scripts/build_android.py`、`scripts/build_native.py`、`scripts/package_release.py`：消费 `--flavor` 参数。
- `.github/workflows/build.yml`：构建矩阵收敛为 `zhenguojian` + `zhenguojian-tv` 两个变体，不再产出红果鉴。

构建命令：

```sh
python scripts/build_android.py --flavor tv   # 真果TV（电视版，全站源）
python scripts/build_android.py               # 手机版（默认 phone，全站源需加 --all-sources）
```

## 二、P0 修复清单

1. **播放器亮度污染**（`lib/player_interactions.dart`、`lib/player_screen.dart`）
   `PlayerInteractions` 增加 `television` 状态，由播放页 `didChangeDependencies` 同步。TV 下短路构造时读亮度、`setBrightnessDirect`、dispose 复位亮度——电视亮度交还系统/遥控器管理。

2. **排序筛选面板**（`lib/catalog_sort_sheet.dart`）
   TV 分支改为居中 `AlertDialog` + `RemoteButton` 网格（选项即时高亮、「应用」统一生效），触屏端保留原 `showModalBottomSheet`。

3. **八页焦点可达**（`lib/settings_screen.dart`、`profiles_screen.dart`、`downloads_screen.dart`、`sources_screen.dart`、`rankings_screen.dart`、`local_media_screen.dart`、`batch_download_screen.dart`、`merge_queue_screen.dart`）
   - 新组件：`TelevisionFocusScroller`（焦点进入子树自动 `ensureVisible`，包在滚动容器外层）、`TvTile`、`TvSwitchTile`、`TvCheckboxTile`（RemoteTarget 焦点壳 + 原视觉，整行 OK 激活）。
   - 各页裸 `ListTile`/`SwitchListTile`/`CheckboxListTile` 全部替换，滚动容器包 Scroller，首项 `autofocus: AppLayout.isTelevision(context)`。
   - `lib/app_layout.dart` `televisionTheme` 补 `listTileTheme`/`radioTheme`/`checkboxTheme`/`switchTheme`/`chipTheme`/`popupMenuTheme`/`dialogTheme` 焦点视觉——修复「焦点看不见」。

4. **系统软键盘清除**（5 处）
   - 新组件：`showTelevisionTextInput` / `TelevisionTextInputDialog`（文本/数字/密码三态自建键盘）、`TelevisionSearchBar`（只读搜索条）。
   - 追剧/历史搜索、下载页搜索 → `TelevisionSearchBar`；选集「跳转集数」→ 数字键盘；用户管理密码/编辑表单 → `showProfilePasswordInput` + `_TvInputRow`。

5. **长按替代入口**（`lib/widgets.dart`）
   TV 下触屏长按（多选下载）不可达，三条替代路径已在位：遥控器菜单键（contextMenu→actions 菜单含「多选下载」）、标题栏「多选下载」按钮、详情页全功能兜底。注释已记录。

## 三、验证状态

- Python 侧：`scripts/test_app_build.py` 在本机的 2 个 error 为既有环境问题（mock `subprocess.run` 后 Windows `platform._syscmd_ver` 拿到 MagicMock），上游原始代码同环境挂 4 个，与本改动无关。`BuildVariant(False,'tv')` 实测输出 `['--flavor','tv','--dart-define=ALL_SOURCES=false','--dart-define=TV_BUILD=true']`、slug `hongguojian-tv`。
- Dart 侧：本机改造时无 Flutter SDK，全部改动待 `flutter analyze` + `flutter test` 终验（工具链就位后执行）。
- 测试兼容性：现有 Dart 测试默认非 TV 模式，走原触屏分支，不受替换影响。

## 四、遗留（P1/P2，未在本轮范围）

- 播放器控制条右侧「播放设置」按钮在极宽内容下可能滚动不可达（`SingleChildScrollView` 不响应 D-pad）。
- TV 全局 10-foot 字号缩放（建议 `textScaler.linear(1.35)`）。
- `RemoteGrid` 行末右键吞键导致 footer「加载更多」不可达。
- rail ↔ 内容网格焦点串联依赖遍历顺序。

## 五、0.2.65+72 红果取数修复（2026-10-24）

背景：红果 2026-10 收紧 App 风控，「目录能看、不能播放」。逐项实测结论：

| 接口 | 旧签名（仅 X-Gorgon） | 完整三签名（Gorgon+Argus+Ladon） |
| --- | --- | --- |
| `video_detail/v1/`（剧集详情） | 200 空体 | **63906 字节，46 集齐全** |
| `category/landpage/v/`（目录） | 200 空体 | **64244 字节，18 行 + 分页游标** |
| `commentapi/comment/list`（弹幕） | — | 正常（原已使用） |
| `video_model/v1/`、`multi_video_model/preload/v1/`（取流） | 200 空体 | 200 空体（官方停供） |

改动：

1. **全接口统一完整三签名**（`native/core/provider_hongguo_app.go`）：目录/详情/弹幕都走 `signHongguoCommentRequest`；不再发送 `X-SS-STUB`（实测空响应与带 STUB 无差别）。删除旧版 `signHongguoRequest`（`provider_hongguo_sign.go` 仅保留设备号生成）。目录与详情从「网页兜底」升级为 App 接口直取（播放量/时长/封面更全）。
2. **预埋两个取流入口**（`provider_hongguo_native_media.go`）：先打 onedrama ADR 0015 配方的 `multi_video_model/preload/v1/`（`video_platform=1024` + `mixed_video_id_map{"1004":[vid]}`，只认请求的 vid），失败再回退旧 `video_model/v1/`。当前两入口均停供，接口重开即自动恢复，无需再发版。
3. **本地播放代理 4 项修复**（移植自上游 PR #2，保留本仓既有的「session.key 播放列表误判」与「红果 m3u8 重定向转透传」修复）：
   - Range 兑现：上游忽略 Range 返 200 全文件时，代理跳过前缀并补 206/Content-Range（修复 seek 后解析错位）；
   - 会话回收：淘汰只回收从未送出数据的会话（预加载/重试类），正在观看的一集不再被误踢成 410；
   - HLS 判定只认 URL 路径后缀，CDN 直链路径带 hls/m3u8 字样不再被误判；
   - Range 请求强制 `Accept-Encoding: identity`，响应头写入后立即 Flush（规避 Media3 8 秒连接超时）；类型确定的响应不再 Peek 阻塞。
4. **移除误提交的签名文件**：`android/key.properties`、`android/duanju_release.keystore`、`android/app/duanju_release.keystore` 曾被提交进这个公开仓库。本次从版本库移除（本机文件保留、已被 .gitignore 覆盖）。这也是 10-23 构建失败的根因——CI 的 `configure_signing.py` 优先使用仓库里的旧 key.properties，签出旧证书，与仓库变量 `ANDROID_CERT_SHA256`（新证书）不符；移除后 CI 改走签名 Secrets。**旧 keystore 已公开暴露，视为泄露，建议轮换。**

验证状态：签名链路用一次性 Python 探针实测（全新设备号、单发、20 秒间隔，2026-10-24）；Go 单测 `app_danmaku_test.go` 的旧断言（普通请求不得带 Argus/Ladon）已按新契约更新；本机无 Go 工具链，`go test -race` 由 CI 执行。

## 六、签名 Secrets 修复与 CI 自检（2026-10-24 续）

10-24 构建（81f11e2）失败于 `:app:packageTvRelease`：`KeytoolException: keystore password was incorrect`。代码侧无问题——`go test -race` 失败集与提交前（5a83d46）完全一致（6 个目录游标既有失败 + 同样 panic；workflow 对测试 `continue-on-error` 不阻塞出包），Flutter 测试 132/23 与 134/20 亦与既有一致，零新增失败。

为定位 Secrets 问题，CI 增加「诊断签名 Secrets」步骤（构建前执行，失败即止，省 6 分钟编译）：

- 打印签名库大小/格式魔数（JKS/PKCS#12）与证书 SHA-256 指纹；
- 校验库密码、密钥密码、别名一致性；
- 密码不符时自动重试「去首尾空白」值，并对 ~50 个常见候选密码本地试探（仅在该库上执行）。

诊断结论（40065057020 / 40065689071 / 40066224453 三次运行）：

- Secrets 中 `ANDROID_KEYSTORE_BASE64` 解出 **2786 字节 PKCS#12 签名库**（非截断、非本机任何现有库——本机旧 JKS 2640、sign3.p12 2658、sudoku.keystore 2718 均不符），应为轮换时上传的新库；
- `ANDROID_KEYSTORE_PASSWORD` 原值与去空白值均无法打开该库，~50 个候选密码也未命中——**密码 Secret 的值不是该库的密码**；
- 本机新备一套签名库 `E:/6/guoapp-signing/duanju_release_2026.keystore`（别名 `duanju`，证书 SHA-256 `f47732e85b889a1c3ac0c727fa79f9a5245315e8d56022b4df93f99e97e27adc`，base64 见同目录 `keystore-base64.txt`；**密码不写入仓库，经聊天单独交付**）作为兜底；采用它需同步把仓库变量 `ANDROID_CERT_SHA256` 改为该指纹。该 keystore 文件与密码是今后更新 App 的唯一凭据，请妥善备份，切勿提交进任何仓库。

用户已决策（2026-10-24）：换用该新签名库。操作路径：GitHub 网页端 Settings → Secrets and variables → Actions，重设四个签名 Secret（ANDROID_KEYSTORE_BASE64 / ANDROID_KEYSTORE_PASSWORD / ANDROID_KEY_ALIAS / ANDROID_KEY_PASSWORD），并把仓库变量 ANDROID_CERT_SHA256 改为上述指纹，随后重跑 Build app packages。当前 PAT 无 Secrets 读写权限，网页操作需用户本人完成。

## 七、登录态取流实测结论（2026-10-24 晚）

用户扫码（番茄小说 passport）成功收割真实登录态（14 个 cookie，含 sessionid/sessionid_ss/sid_tt，x-tt-token 取自 sessionid_ss）后，对全部取流入口做登录态 vs 游客态对照实测，**结论：登录态对取流零帮助**。

| 入口 | 游客态 | 登录态 |
| --- | --- | --- |
| multi_video_model/v1/（hongguo-web 审核通道完整复刻：a 后缀主机 + channel=audit + device_type=audit-windows + UA hongguo-route-audit/1.0 + key"1"） | 0 字节（上午为 110001，下午已加重为断流） | **0 字节** |
| multi_video_model/preload/v1/（onedrama 配方 key1004） | 0 字节 | **0 字节** |
| video_model/v1/ 旧版（video_platform=3） | 0 字节 | **0 字节** |
| video_model/v1/ 旧版（video_platform=1024） | — | **0 字节** |

全部返回 HTTP 200 + 空 body，与账号无关，系红果官方服务端内容级断流。hongguo-web（sgccdev，10-06 停更）的同款路径同样已断——行业性封锁，非本 App 问题。取流双入口（preload + legacy）保持预埋，接口重开即自动恢复，无需再发版。

## 八、内建七猫短剧源（v0.2.66+73，2026-10-25）

红果官方内容级断流、登录态实测无法破解（见第七节），因此在 App 内直接新增一个**可用站源**：七猫短剧（qmplaylet）。不依赖红果任何接口，HLS 直链走公网 CDN。

### 接口与签名（Python 探针 + Go 实现双向验证）

- 目录：`https://api-store.qmplaylet.com/api/v1/playlet/index?tag_id=<tag>&playlet_privacy=1&operation=1&sign=md5("operation=1playlet_privacy=1tag_id=" + tag + KEYS)`
- 详情：`https://api-read.qmplaylet.com/player/api/v1/playlet/info?playlet_id=<id>&sign=md5("playlet_id=" + id + KEYS)`
- 请求头：`qm-params` = 设备 JSON 的 base64 经 64 字符表逐字符替换；`sign` = md5(`AUTHORIZATION=app-version=10001application-id=com.duoduo.readchannel=unknownis-white=net-env=5platform=androidqm-params=<qm>reg=<KEYS>`)；另需 net-env/reg/channel/is-white/platform/application-id/authorization/app-version、UA `okhttp/3.12.11`
- `KEYS = d3dGiJc651gSQ8w1`，字符表见 `native/core/provider_qimao.go` 的 `qimaoCharMap`

### 实测结论（本机 Go 直连，非 Mock）

| 环节 | 结果 |
| --- | --- |
| 分类 | 18 个（新剧 / 都市情感 / 逆袭 / 虐恋 / 复仇 / 豪门总裁 / 古装 / 重生 / 甜宠 …，源自接口 `tag_items` 149 项里的前 18 个） |
| 目录 | tag 0「全部」66 部；单分类 15~19 部；**接口不支持分页**（加 page/offset 一律返回 0 部）→ 第 2 页返回空、hasMore=false |
| 详情 | `data` 即剧集信息，`play_list` 逐集 video_id/video_url/duration/sort |
| 取流 | `resolveQimaoMedia` → HLS 拉取成功（3778 字节播放列表，时长 2m3s），CDN 为 `cdn-vod-playlet.wtzw.com` |
| 搜索 | 官方搜索接口已停用（各变体均返回 `{"list":null}`）→ 改为「推荐页标题匹配 + 命中分类名整类返回」，实测「总裁」命中 22 部 |

### 代码改动

- 新增 `native/core/provider_qimao.go`（约 600 行）：签名、目录/分类/详情/搜索/取流，仿 `provider_huangju.go` 的错误处理与限流写法
- 接线：`provider_huangguo.go`（源常量 + 白名单 + 别名 `qimao/qmplaylet/qmplaylet.com` + `GetHuangguoChapters`）、`app_runtime.go`（搜索分支 + 目录 switch + 空结果兜底 + 详情 switch + 集数合并）、`app_categories.go`（分类校验 + 分类拉取）、`provider_media.go`（baseURL + 取流分发）、`app_cover_metadata.go`（封面补齐）
- `lib/models.dart`：新增 `SourceSite('qimao', '七猫', '短剧 · 分类 · 搜索')`，放入 `primaryValues`（默认可见，无需解锁密码），并加入 `pagedSearch`
- 本地校验：`go build ./...`、`go vet ./core/`、`go test -run "TestNativeCategories|TestSource|TestProvider"` 全部通过（本机新装 Go 1.24.6 于 `E:/6/tools/go`）

## 九、红果播放真正修好：移植播放专用签名（v0.2.67+74，2026-10-25）

第七节曾判定「登录态无效、红果服务端内容级断流」。**该结论是错的**——补上正确的请求签名后，红果四个接口全部恢复返回真实数据，无需登录。

### 根因

红果自 2026-10 起对 App 接口校验 X-Gorgon + X-Argus + X-Ladon 三签名，且**播放/目录与弹幕用的是两套不同签名**：

| 差异点 | 弹幕签名（本仓库原有） | 播放签名（本次新增） |
| --- | --- | --- |
| Argus 字段 4（aid） | `3019` | `8662` |
| Argus 字段 5（device_id） | **缺** | 取 query 的 `device_id` |
| Argus 字段 6 | `1611921764` | `1611921764` |
| Argus 字段 7（版本） | `6.8.1.32` | `7.3.5.32` |
| Argus 字段 11 / 16 | **缺** | `0` / 空字节 |
| Argus 字段 23（机型环境） | **缺** | `{1:"25053RT47C",2:"16",3:"update_64",4:2147483648}` |
| Argus 字段 25 | **缺** | `2` |
| Argus 字段 3 掩码 | 原值 | `& 0x7fffffff` |
| body 摘要（字段 13） | `SM3(16 个 0)` | `SM3(MD5(body))`，并写 `X-SS-STUB` |
| 字段 15 计数器 | 嵌套 `{2,3,4}=1388734` | `{1:1,2:1,3:1,7:3348294860}` |
| X-Gorgon 混淆 | RC4 式 + `{0,1,7,4}` | 固定密钥异或 + `{0,6,11,28}` |
| X-Ladon 载荷 | `ts-1611921764-3019` | `ts-1611921764-<aid>` |

原实现把所有请求（含目录/详情/取流）都按弹幕签名发，Argus 缺 device_id 与机型环境，网关一律回 HTTP 200 + 0 字节——这正是第七节误判为「断流」的现象。

### 实测（本机 Go 直连真实网关，非 Mock）

| 入口 | 结果 |
| --- | --- |
| `/novel/player/video_detail/v1/` | 200，63902 字节，code=0 |
| `/reading/distribution/category/landpage/v/` | 200，53191 字节，has_more=true |
| `/novel/player/multi_video_model/preload/v1/` | 200，23488 字节，video_model status=10 |
| `/novel/player/video_model/v1/` | 200，23466 字节，status=10 |
| `resolveHongguoAppMedia` 生产链路 | 拿到直链，画质 1080，时长 1m48s |
| 直链 Range 拉取 | **HTTP 206，读到真实 MP4 头 `ftypisom`** |

### 代码改动

- 新增 `native/core/provider_hongguo_player_sign.go`（162 行）：播放签名（X-Gorgon / X-Argus / X-Ladon），移植自上游 Xanerix/guoapp 0.2.78+88 的 `fix: 红果播放签名补全字段`
- `provider_hongguo_app.go`：非弹幕请求改走播放签名，弹幕仍走弹幕签名；非弹幕分支不再预生成弹幕 nonce
- 本地校验：`go build ./...`、`go vet ./core/` 通过；`go test ./core/` 中 `TestNativeCatalogMetadataSaveExcludesInFlightCursor` 在本改动之前即已失败（已用 `git checkout` 回退验证基线，非本次回归）

## 十、TV 端 UI 重设计 v1（v0.2.68+75，2026-10-25）

设计稿与规范见 `design/tv-redesign-v1.html`、`design/design-contract.md`（1920×1080 等比，
浏览器里可用 ←→↑↓ / Enter / Esc 等价遥控器验证）。本轮把设计稿落到真机代码。

### 改动清单

| 文件 | 变化 |
| --- | --- |
| `lib/design_tokens.dart`（新增） | 色板 `TvColor`、密度与焦点参数 `TvSpace`、字阶 `TvText`，以及反相/提亮/降亮三个 `ColorFilter` |
| `lib/app_theme.dart` | 深色画布 `#101114 → #08090C`，品牌 `#FF664F → #FF6B4A`，surface 全系换成 `#14161D/#1B1E27/#242833`；TV 端 `mode()` 强制深色；InputDecoration 圆角 16 → 12 |
| `lib/app_layout.dart` | `televisionTheme` 焦点改为「获焦整块反白」（白底黑字+反相），按钮改 pill；popup/dialog 表面去 tint |
| `lib/remote_widgets.dart` | `RemoteTarget` 焦点三重可辨：`AnimatedScale(1.065)` + 3px 白环 + `ColorFilter` 提亮 1.1 + 珊瑚外发光；新增 `invertOnFocus` / `focusScale` / `glow`。`RemoteButton` 改 pill，新增 `pill` / `dense` |
| `lib/widgets.dart` | `DramaTile` 在 TV 下缩略图改 **16:9 横版**（手机仍 2:3）；白环只包画面不包标题；`extentFor` 同步算入焦点内边距；`dramaGridDelegate` TV 下按 300 宽分列（3–7 列） |
| `lib/home_screen.dart` | 左侧导航改**浮动玻璃栏**：收起 118 / 任一子项获焦展开 300，渐变材质 + 发丝边框，选中项珊瑚 tint，替换原 164px 纯按钮列 |
| `lib/player_controls.dart` | 进度条 `trackHeight 3→8`、圆钮 `6→11`、热区 `14→22`、行高 `28→36`；音量竖条同样规格；进度/圆钮改品牌珊瑚 |

### 刻意没做的（避免布局风险）

- **字号全部不动**。设计契约按 960dp 折算（如卡片标题 11sp），但 TV 各家 density 不一
  （960dp 与 1920dp 都存在），硬改字号会在某类机型上突然变小。本轮只调字重、行高、明度。
- **详情页 / 播放页 OSD 结构未拆**。设计稿的详情页是「海报 + 信息列 + 选集贴底」新结构，
  涉及 `detail_screen.dart`（912 行），留待 v2 单独做，避免与皮肤层改动混在一起难以回滚。
- **浅色主题保留在代码里但 TV 不可达**（`AppTheme.mode(television: true)` 直接返回深色）。

### 推送方式变更（本轮遇到的环境问题）

`git push` 到 `github.com` 被 TLS 阻断（`schannel: failed to receive handshake` /
`OpenSSL: unexpected eof`），但 `api.github.com` 经内置代理 `127.0.0.1:58366` 正常。
因此新增 `tools/push_via_api.py`：用 Git Data API 手工构造 blob → tree → commit → ref 更新，
绕过 `github.com`。配套 `tools/ci_status.py`、`tools/wait_ci.py` 查运行状态。
副作用：远程 commit 对象本地不存在，本地 `main` 与 `origin/main` 在哈希层面分叉（内容一致），
网络恢复后 `git fetch` 即可对齐。

## 十一、两端版本线拆开 + 手机端回归修复（电视 1.0.0 / 手机 0.2.69，2026-10-25）

用户确认 v0.2.68+75 的 TV 界面可用，并提出：**电视端与手机端分开更新，电视端定为正式版 1.0，
手机端继续沿用 0.2.x**。

### 1. 版本线拆分

| 端 | 版本来源 | 当前值 | Release 标签 |
| --- | --- | --- | --- |
| 电视端 | `versions.json` 的 `tv` | `1.0.0` | `tv-v1.0.0` |
| 手机端 | `pubspec.yaml` 的 `version` | `0.2.69` | `app-v0.2.69` |

- 两端**共用** `pubspec.yaml` 的 `+N` 构建号 —— 它决定 versionCode，必须只增不减，
  否则系统会拒绝覆盖安装。当前 `0.2.69+76`。
- 电视端版本名由 `scripts/build_android.py` 传 `--build-name` 覆盖（Gradle 里的
  `versionNameSuffix = "-tv"` 仍生效，最终为 `1.0.0-tv`）；手机端不传，直接用 pubspec。
- **刻意不传 `--build-number`**：一旦两端各自从 1 起算，versionCode 会回退，覆盖安装必失败。
- App 内「关于」显示的版本号改为编译期注入 `--dart-define=APP_VERSION=<flavor 版本>`，
  不再把版本号写死在 `lib/app_layout.dart`（原来写死 `'0.2.68'`，电视端会显示错）。
- CI 的 release job 从「一个 Release 装两端的包」改成 `publish()` 函数循环两次，
  各发各的 Release、各生成各的 `SHA256SUMS.txt`；电视端后发，保证 Releases 页首条是电视版。

**versionCode 已实测确认**：`--split-per-abi` 会给每个 ABI 叠不同基数 ——
armeabi-v7a `+1000`、arm64-v8a `+2000`、x86_64 `+4000`，所以 0.2.68+75 的三个包分别是
1075 / 2075 / 4075（不是之前以为的统一 1075）。因此 0.2.69+76 会得到 1076 / 2076 / 4076，
链路单调递增，覆盖安装安全。

### 2. 手机端被误伤的两处（本次修复）

上一轮做 TV 皮肤时，有两处改动写在了**手机与电视共用的基础层**里：

| 位置 | 问题 | 修复 |
| --- | --- | --- |
| `lib/app_theme.dart` 的 `_theme()` | `splashFactory: NoSplash.splashFactory` 写进基础主题 → 手机点任何东西都没有水波纹反馈 | 移到 `app_layout.dart` 的 `televisionTheme()`，只在电视生效 |
| `lib/player_controls.dart` 进度条 / 音量条 | `trackHeight 3→8`、圆钮 `6→11`、热区 `14→22` 全局生效 → 手机上进度条粗一倍 | 按 `AppLayout.isTelevision(context)` 二选一，手机回到 3 / 6 / 14 |

补了一条注释踩坑提醒：`focusColor` / `splashFactory` 这类电视专属覆盖不要写进共用基础主题。

### 3. 确认手机端其余部分未受影响

- 手机 flavor（`com.duanju.zhenguojian`）CI 构建一直全绿，主清单 `leanback required=false`，
  手机可正常安装；与电视版不同包名，互不覆盖。
- 主题浅色分支保持原值（`#FAF8F6` / 白色表面），只有深色分支换成新的近黑画布。
- 首页导航、卡片比例、焦点样式都是 `television` 分支内改动，手机走原有 `InkWell` + 2:3 竖版海报。

## 十二、红果全挂的根因：入口域名被按 SNI 阻断（TV 1.0.1 / 手机 0.2.70，2026-10-25）

### 现象

用户两台设备（电视 + 雷电模拟器）同时报：

```
Get "https://hongguoduanju.com/search/%E6%98%A5%E6%9A%A7?...":
    read tcp ...->47.97.226.443:443: read: connection reset by peer
Get "https://v.fengbao14.com/munaiyxnlng/.../index.m3u8":
    read tcp ...->37.87.226.443:443: read: connection reset by peer
```

### 排查过程与结论

**不是 App 的 bug，是网络层按 TLS SNI 精确阻断。** 关键实验（同一 IP、同一台机器、只改 SNI）：

| 发出去的 SNI | TLS 握手 |
| --- | --- |
| `hongguoduanju.com` | ❌ 立刻被 RST |
| `www.hongguoduanju.com` | ✅ 成功 |
| `example.com` | ✅ 成功 |
| 不发 SNI | ✅ 成功 |
| `hongguoduanju.com.`（尾点） | ✅ 成功（但 **Go 会主动去掉尾点再发**，不可用） |

服务器本身完好：假 SNI 拿到的 HTML 里含 `8662`（红果 aid），`/search` 路径返回 37 KB 正常结果。
Go 的 `hostnameInSNI()` 按 RFC 6066 会裁掉尾点，所以尾点方案在 Go 里无效，只能改子域。

**只有裸域名被封，红果其他链路全部正常：**

| 域名 | 用途 | 状态 |
| --- | --- | --- |
| `hongguoduanju.com` | 首页 / 分区 / 搜索 | ❌ SNI 阻断 |
| `api5-normal-sinfonlineb.fqnovel.com` | App 接口 | ✅ |
| `novel.snssdk.com` | 媒体 | ✅ |
| `djapi.999888456.xyz` | 播放 | ✅ |
| `api-store/api-read.qmplaylet.com`、`cdn-vod-playlet.wtzw.com` | 七猫全链路 | ✅ |

`v.fengbao14.com`（外部订阅源“三片”）是境外站，同一机制，与 App 无关。

> 附带发现：本机 **UDP 53 被本地劫持**（向 223.5.5.5 用 UDP 查得 `1.1.1.1`，改用 TCP 才拿到 12 个真实 CDN 节点）。这解释了为什么模拟器解析到 `47.97.226.443`、宿主机解析到 `101.226.41.x` —— 不同解析路径给了不同节点。

### 修复

新增 `native/core/network_hongguo_sni.go`：只对 `hongguoduanju.com` 改用
`www.hongguoduanju.com` 作为握手 SNI，**URL / Host 头 / 证书校验全部不变**（该服务器证书同样覆盖 www），
其余域名原样透传，CDN 优选、代理、浏览器指纹链路都不受影响。

- 伪装通道失败自动回落原通道，并进入 3 分钟冷却，避免每个请求白等一次超时；
  冷却后自动重试，网络恢复即自行回到伪装通道。
- 刻意**没有**用 `InsecureSkipVerify`，也没有降 TLS 版本。

实测（`LIVE_NET_TEST=1 go test ./core/ -run HongguoSni`）：

| 路径 | 真 SNI | 伪装 SNI |
| --- | --- | --- |
| `/` | ❌ RST | ✅ 200 / 155 KB |
| `/category/real-drama` | ❌ RST | ✅ 200 / 255 KB |
| `/category/ai-drama` | ❌ RST | ✅ 200 / 190 KB |
| `/search/春晧` | ❌ RST | ✅ 200 / 37 KB |

配套单测：`TestHongguoSniMaskRoutesOnlyBlockedHost`（只分流目标域名）、
`TestHongguoSniMaskFallsBackWhenMaskedFails`（失败回落 + 冷却）。
真实网络验证 `TestHongguoSniMaskLive` 默认 skip，需显式设 `LIVE_NET_TEST=1`。

## 十三、手机端红果无法播放：CENC 加密，改用 libmpv 解密（TV 1.0.2 / 手机 0.2.71）

**现象**：手机上（装 1.0.1 或 0.2.70 都一样）红果列表与封面正常，一点播放就
`Playback error / Source error`，`VideoError`，有时伴 `Invalid NAL length
{contentIsMalformed=true, dataType=1}`；换成移动数据也一样。

**定位过程**：抓 logcat 得到链路全绿——本地代理返回 `206 video/mp4`，
ExoPlayer 甚至已解析出 `hvc1.1.6.L186 / 1080x1880 / 30fps / 657kbps`，
说明**网络、SNI 掩码、取流地址都没问题**，坏的是 mdat 里的样本字节。

随后通过 `adb forward` 抢下代理吐出的完整 9,103,710 字节 mp4，解包确认：

```
ftyp isom → moov → trak/stbl/stsd → encv(hvcC + sinf) → … → senc / saiz / saio / sgpd / sbgp
sinf.frma = hvc1   sinf.schm = cenc(v1.0)
sinf.schi.tenc: default_isProtected=1  per_sample_IV_size=8  KID=6adb7223…
senc: version 0 flags 0x0（无 subsample），sample_count=2986
```

也就是**标准 CENC（AES-128-CTR 全样本加密）**。`moov` 是明文的，所以播放器能解析出
编码参数；`mdat` 是密文，所以一读样本就报错。这类文件的正确播放前提是
**把 content key 交给解复用器**，ffmpeg 靠 `decryption_key`，ExoPlayer 靠
`DrmSessionManager`（`video_player` 插件不暴露）。

**根因**：`player_screen.dart` 里后端选择写成「Android 且非 TV → `LunaExoPlayer()`」，
手机因此永远走 ExoPlayer，而 Go 侧解出来的 `CENCKey` 只在 libmpv 分支被用作
`demuxer-lavf-o=decryption_key=…`。电视一直正常，正是因为电视走的是 mpv。

**修复**：后端选择由「是不是手机」改为「这一集要不要解密」：

```dart
Platform.isAndroid && !AppDevice.isTv && !_sourceNeedsDecryption
    ? LunaExoPlayer()
    : Player(…)   // libmpv
```

`_sourceNeedsDecryption => widget.detail.drama.source == 'hongguo'`。红果在手机与电视上
都走 mpv + decryption_key；其余站源保持 ExoPlayer 不变（硬解、画中画都不受影响）。
另在 `_play` 里打一行 `[Play] 后端=… 加密key=…` 便于日后回归时一眼确认走的是哪条路。

配套：`tools/grab_proxy.py` 重做——按 logcat 里出现的 URL 动态 `adb forward`，
并发抢 `head1m / head64k / full` 三个区间，用来在会话被销毁前把原始字节留下来。

---

## 十四、验证：手机端红果已恢复播放（2026-10-25 18:29 实测）

实测机型 **Redmi K40（M2012K11AC / Android 13 / arm64-v8a）**，无线调试安装
`zhenguojian-tv-1.0.2-arm64-v8a.apk`（versionCode 2078，覆盖原 1.0.1 / 2077，数据保留）。
播放两集后 logcat：

```
[Play] 后端=libmpv 加密key=32 位 hex
[Play] 调用 _player.open: …/0d7f7bbb6ecd010bdbc223e7.mp4
[Play] 后端=libmpv 加密key=32 位 hex
[Play] 调用 _player.open: …/fc7d8b2c0224befbc53943f3.mp4
```

两集连播、自动接续均正常。同一时段其它 HLS 站源仍是 `后端=ExoPlayer 加密key=无`，
硬解路径未受回归影响。**手机端红果「无法播放」确认由 CENC 加密 + 后端选错共同导致，已闭环。**

附：验证「修复是否真的编进包」的有效手法——Dart AOT 快照里非 Latin-1 字符串以
**UTF-16LE** 保存，用 UTF-8 搜 `加密key=` 会得到 MISSING 的假结论；应按
`'加密key='.encode('utf-16-le')` 在 `lib/arm64-v8a/libapp.so` 里查找。

**待发版**：`lib/main.dart` 里的启动日记把版本号硬编码成了 `0.2.64+71`（与实际版本无关，
排查时容易误判），已改为读 `appVersion`（即 `--dart-define=APP_VERSION`）。此改动尚未提交，
随下一次发版一起带上。

---

## 十五、红果搜索「暂时无法加载」修复（2026-10-25 19:00）

**实测结论（掩码 SNI 直连 hongguoduanju.com，脚本 `tools/diag_hongguo_search.py`）**

| 请求 | 结果 |
|---|---|
| `/search/都市` | 200，37199 B，正文是 `<div id="root"><!--<?- html ?>--></div>` 纯模板壳，**无 routerData / searchList** |
| 同上换 UA（iPhone / Android / 桌面）、路径变体、带 ttwid cookie 重放 | 全部同样空壳 |
| `/`（首页，143 KB）、`/detail?series_id=` | routerData 正常，仍是服务端渲染 |
| `/incent_resource/suggestion?app_id=8662&query=都市` | 200，20 条，其中 `short_play_name` 才带 `video_data.series_id`（3~8 条） |
| `/incent_resource/search`、`search_series`、`series_search`、`/api/search*` 等 18 个候选 | 全部 404 |

即：**红果把搜索页改成了客户端渲染（React SPA，chunk `search.fe0ab865c5.js`），HTML 里不再内嵌结果**；
首页与详情未受影响，所以只有搜索坏。

**根因**：搜索有两条腿——网页 HTML（`page`）与联想补名（`names`）。`page` 因 SPA 化必然失败；
`names` 受 `hongguoSuggestionTimeout = 5s` 限制，而 SNI 掩码通道冷连经常超过 5s，
于是两条腿同时为空，`_items.isEmpty && _error != null` → 界面显示「暂时无法加载」。

**修复**
1. 搜索补名超时 5s → 15s（新增 `hongguoSearchNameTimeout`，作为参数传入
   `fetchHongguoSuggestionRecords`）；UI 联想下拉仍用 5s。
2. 剧集判定改为「`series_id` 合法即收」，去掉 `word_type == short_play_name` 白名单
   （红果调整过类型枚举，白名单会把结果静默过滤干净）。
3. 新增**二级联想**：把一级返回的剧名词 / 分类词 / 热词再各查一次联想（最多 5 个并发），
   实测能把命中从 3 条提到十几条；补季路径改走 `fetchHongguoSearchNamesLight`
   （只一级），否则 32 次补季查询会把请求量放大到近 200 个。
4. 页面返回 SSR 空壳时直接报「红果搜索页已改为客户端渲染，页面不再内嵌结果」，不再笼统报"未返回有效结果"。
5. 联想结果 ≥6 条时不再挂"搜索页已改版"的提示，避免每次搜索都像出故障。
6. `home_screen` 的搜索成功 / 失败都写一条 `[Search]` 日记，下次不接调试线也能定位。

---

## 十六、七猫搜索接回官方接口 + 鬼片多线路/掩码通道（2026-10-26）

### 1. 七猫：搜索接口其实一直存在（TV/手机都受影响）

旧代码注释写「七猫搜索接口已停用」，改成在推荐页 62 条里做标题匹配 —— 实测
`都市/霸道/穿越` 这类词命中 **0~1 条**，所以「搜不到」是常态。

盲测 18 个候选路径后拿到真接口：

| 项 | 值 |
|---|---|
| 路径 | `GET https://api-store.qmplaylet.com/api/v1/playlet/search` |
| 关键词参数 | **`wd`**（不是 keyword/query；用错参数名会返回 `list: null`） |
| 签名 | `md5(参数名字母序拼接 + "d3dGiJc651gSQ8w1")`，即 `page=<n>wd=<词>` |
| 返回 | 每页 10 条，`meta.next_page` 指示还有下一页（「都市」实测 total_pages=289） |
| 字段差异 | 搜索给 `id`（目录给 `playlet_id`）、集数在 `total_num`（形如 `78集`）、标题带 `<font>` 高亮标签、题材在 `sub_title` |
| 详情可复用 | `api-read…/player/api/v1/playlet/info?playlet_id=<id>` 直接可用（「都市无上仙尊」78 集全部可取到 video_url） |

改法：新增 `qimaoSearchAddress/qimaoSearchRows/searchQimaoNative`，搜索走真接口并支持翻页；
接口异常或当页空结果时才退回旧的标题匹配兜底。**注意**：接口正常但无匹配时返回空列表（不再挂
warning），否则会走 `catalog_browser` 的 AppFailure 分支显示「暂时无法加载」。

### 2. 鬼片：不是解析坏了，是 CDN 被 DNS 劫持 + SNI 阻断（双重）

实测（`tools/diag_guipian.py`、`tools/diag_guipian_lines.py`、`tools/diag_guipian_cdn.py`）：

| 环节 | 结果 |
|---|---|
| 列表页 / 详情页 / 播放页 | 200，5 条线路、分集链接齐全，`var now` 正常给出 m3u8 |
| 线路 CDN 域名 | `v1.qqqrst.com` / `v10.ppqrrs.com` / `v13.rstu6.com` … 系统 DNS 一律解析到 **183.192.65.101**（上海市反诈提示页，HTTP 200 + text/html，伪装成正常响应） |
| 真实 IP | 只能靠 **TCP 53** 问 223.5.5.5 / 114.114.114.114 才拿得到（216.180.x.x 段） |
| 直连真实 IP + 真 SNI | TLS 握手期被 RST |
| 真实 IP + **裸域 SNI**（`rstu6.com`）+ Host 仍是 `v13.rstu6.com` | **200 + 真 m3u8**，可下到分片（`ts` 409600B，首字节 0x47） |
| 证书 | 这些 CDN 本来就是跨域证书（`rstu6.com` 的 IP 返回 `*.bxgbnet.com`），校验证书对它们没有意义 |

因此做了两件事：

1. **播放阶段自动换线**：`resolveGuipianMedia` 按「上次可用线路 → 本集原线路 → 其余线路」
   逐个试（最多 5 条，每条 8s），并记住每部剧可用的线路号（`guipianLines`），
   避免每次都从被封的第一条线重试。
2. **CDN 掩码通道**（`native/core/network_cdn_mask.go`）：主机名形如 `v<数字>.<域>` 时，
   改用 DoH 取到的真实 IP + 裸域 SNI + 跳过证书校验建连，Host 头保持不变；
   只对这类主机启用，其余域名完全透传，掩码失败会有 3 分钟冷却并回落原链路。

### 3. 界面：搜索 0 条不再显示「暂时无法加载」

`home_screen` 里 `_error` 同时承载「真失败」和「降级提示」，导致 0 结果被渲染成错误页。
新增 `_failed` 只在 catch 里置位：真失败才显示「暂时无法加载」，0 结果显示「没有找到匹配的短剧」。

---

## 十七、七猫「搜不出东西」的真正原因：遥控器只能打拼音（TV 1.0.5 / 手机 0.2.74，2026-10-26）

上一版把七猫搜索接口接回来了，但盒子实测仍然「能搜但不能用」。查下来不是接口问题，
是**输入方式**问题。

### 1. 根因：七猫搜索只认汉字，而电视只有遥控器

实测（`tools/diag_qimao_suggest.py`）：

| 输入 | 结果 |
|---|---|
| `wd=都市` | 10 条 |
| `wd=dushi` | **0 条** |
| `wd=ds` / `wd=DUSHI` / `wd=dushi&search_type=2` | **0 条** |

七猫没有任何提示词接口（盲测 15 个候选路径全 `404 page not found`），`searchSuggestions`
又只对红果开放（`models.dart` 里写死 `id == 'hongguo'`）。而电视端的搜索弹窗
（`remote_widgets.dart`）按字母键盘输入，提交时靠一句
`if (_results.isNotEmpty && 纯字母) target = _results.first` 把拼音替换成联想里的中文名
——**没有联想就等于没法把拼音变成汉字**，字母原样提交必然 0 条，界面显示「没有找到匹配的短剧」。

### 2. 两处改动

1. **前端开联想**：`searchSuggestions` 从「仅红果」放宽到「所有在线搜索站源」（`=> onlineSearch`）。
   联想词库来自红果的联想接口——它本身就是拼音检索，`shanhun → 闪婚…`、`zongcai → 总裁…`、
   `zhanshen → 斩神/战神` 都实测能转，返回的是通用短剧词/剧名，与站源无关，可以直接借来当拼音词库。
2. **后端加拼音兜底**（`native/core/search_pinyin.go`）：七猫搜索接口本身正常但返回 0 条、
   且关键词是**纯字母拼音**时，先把拼音转成最多 3 个中文候选词（按字数从短到长排，短词在别的站源更容易命中）
   再各搜一次，命中即返回。转写结果缓存 10 分钟，避免每个按键都打一次联想。
   这样即使不走联想下拉（比如手机端直接打字母），也能搜出东西。

顺带验证了七猫搜索是**模糊匹配**：`都市逆袭 / 闪婚 / 总裁 / 斩神` 这些词都能回 10 条，
所以任意一个转写出来的中文词都能落到结果页，不会出现「转写成功但依然 0 条」。

### 3. 顺手修掉红果搜索的同类隐患

红果搜索页已改成 React SPA，`fetchHongguoSearchPage` 恒返回错误。旧逻辑把这个错误也当成
「搜索失败」参与判定，于是**任何 0 命中的关键词都会被判成异常**，`catalog_browser` 直接抛
`AppFailure` → 界面「暂时无法加载」，而实际只是没有匹配结果。改法：

- 只有「联想腿也失败且两条腿都没拿到剧集」才算真失败，SPA 页的错误不再参与判定；
- `Limited` 只在有结果时置位，0 命中不再附带「已显示当前可获取的匹配结果」的提示语；
- 提示语改成：联想失败才说「名称检索暂不可用」，SPA 页退化且结果偏少时才说「搜索页已改版」。

同步新增诊断脚本：`tools/diag_hongguo_suggest.py`（联想接口 / 拼音转写能力）、
`tools/diag_hongguo_spa.py`（从 SPA 的 JS bundle 里挖接口路径）、
`tools/diag_qimao_suggest.py`（七猫拼音支持 + 候选提示词端点）。

---

## 十八、新装后「头两分钟全体无法加载」的根因修复（TV 1.0.6 / 手机 0.2.75，2026-10-26）

用户报：新装 App 后刚开始用，搜索、放片、连返回首页都容易「暂时无法加载」，
过 2–3 分钟自己恢复正常。定位下来不是网络差，是**三个「带过期时间的惩罚状态」**
把唯一可用的通路自己关掉了。

### 1. 根因：把唯一通路当成「可选优化线路」处理

红果入口 `hongguoduanju.com` 在用户网络里被按 SNI 精确阻断（TCP 能连、握手期被 RST），
所以 `network_hongguo_sni.go` 给它开了一条「伪装 SNI 通道」（ClientHello 里发
`www.hongguoduanju.com`）。这条通道是**唯一能通的路径**。

旧逻辑：伪装通道失败一次 → `degradedUntil = now + 3min` → 回落 base。
而 base 对这个域名本来就是死路，回落等于保证失败。于是：

- 红果剧库、搜索页、**拼音联想**（遥控器打拼音转中文全靠它）、播放凭证、返回首页
  全在同一个域名上 → 3 分钟里一起死；
- 「2–3 分钟自愈」正是这个 3 分钟常量；
- 「新装才明显」的机制：`hongguoSniMaskedDialer` 拨号超时 20s > catalog 请求 ctx 12s，
  冷启动那次握手最容易被 ctx 砍掉，于是一次**请求超时**就被记成「通道失效」→ 锁 3 分钟。

鬼片 CDN（`network_cdn_mask.go`）是同一个毛病，且更糟：这些域名 DNS 被劫持，
回落 base 拿到的是 HTTP 200 + text/html 的反诈提示页，传输层看起来「成功」。

### 2. 改动（`native/core/network_mask_state.go` 为共用状态机）

1. **退避从分钟级改成秒级阶梯**：1s → 2s → 3s → 5s → 8s → 15s 封顶，
   连续失败才逐级拉长，成功一次立刻归零。冷却期不再回落 base。
2. **冷却所剩 ≤3s 时原地等完再试**（`maskTransparentWait`），
   把瞬时抖动变成对用户不可见，而不是弹一次「暂时无法加载」。
3. **本地取消不算通道故障**：前端每次切分类 / 重新搜索 / 退出都会
   `cancelCatalog()`，旧实现把它也记成伪装通道故障 —— 切两下页面就能锁 3 分钟。
   现在 `context.Canceled` 直接放过，不计数。
4. **拨号超时 20s → 10s**，与 12s 的请求 ctx 留出余量，减少「超时被误判成通道失效」。
5. **回落 base 只在探测确认可用时才发生**，且按域名类型分开判定：
   页面类（红果入口，本来就是 HTML 站点）只看状态码 `maskBaseStatusUsable`；
   媒体类（鬼片 CDN）额外排除 `text/html` `maskBaseMediaUsable`，避免把被劫持
   拿到的反诈页当视频流往下传。
6. **403 / 429 冷却同样改阶梯**（`backoffLadder`）：旧实现一律锁 60 秒，
   首屏十几张封面里只要一张被 403，整个域名就被锁一分钟。
   现在 5s 起步，连续被拒才升到 10/20/40/60 秒；`Retry-After` 只能加长不能缩短。
7. **前端加「秒级失败自动重试一次」**（`home_screen.dart`）：
   失败耗时 < 2s 说明请求很可能根本没发出去（冷却 / 退避快速失败），
   等 1.5 秒自动重试一次，避免第一眼就是「暂时无法加载」；重试前记一条 Diary 便于定位。

### 3. 新增回归测试（`network_hongguo_sni_test.go`）

- `TestHongguoSniMaskRejectsFailedBase`：base 返回 403 时必须判失败、且不再重复回落；
- `TestMaskBaseUsabilityChecks`：页面类/媒体类对 HTML 与 m3u8 的判定差异；
- `TestHongguoSniMaskIgnoresCancel`：连续三次本地取消不触发冷却、不回落；
- `TestHongguoSniMaskCooldownFailsFast`：冷却期快速失败（< 1s），错误文案带剩余秒数；
- `TestBackoffLadderEscalates`：5/10/20/40/60 阶梯，Retry-After 只加长不缩短。

顺带发出上一版压在工作区的两处小改动：`helpers.go` 把 `context canceled` /
`context deadline exceeded` 翻成中文，`app_catalog_cache.go` 保存剧库的告警改走
`nativeSaveError`，不再把裸英文串漏进界面。

## 十九、新增第 6 个站源「暴风资源」（TV 1.0.7 / 手机 0.2.76，2026-10-28）

### 1. 起点：绿泡泡调研，结论是不可接入

用户先问「GitHub 上绿泡泡视频的播放源能不能整合进来」。查完结论是**不能**，原因在结构层面：

- 绿泡泡短剧是腾讯在微信内上线的免费短剧小程序（IAA 模式，对标红果），**没有独立 App、没有网页版**；
- 小程序接口全部要登录态：`wx.login` 拿 code → 服务端换 `session_key` → 后续每个请求带 token。
  第三方 App 里没有微信登录态，也拿不到 code；
- 视频走腾讯 CDN，链路带**时效签名**并绑定 referer / UA / 设备指纹，脱离小程序语境直接 403；
- GitHub 上用中文名、拼音（`lvpaopao` / `greebubble` / `wxduanju` / `wechat-drama`）等 10 多组词搜过，
  只有两类结果：`miyu` / CipherTalk（微信聊天记录解密工具，与视频无关）和
  `DerekNazh/wechat-video-drama-downloader`（本地代理嗅探，许可证写明「私有项目，未经授权禁止使用」，
  没有任何可复用的源）。

**没有任何匿名可调用接口，也没有可提取的源。** 所以改接一个结构上真正开放的源：暴风资源。

### 2. 暴风源：苹果 CMS v10 采集接口

`https://bfzyapi.com/api.php/provide/vod/`，标准苹果 CMS v10 采集协议——明文 JSON、
免登录、免签名，`vod_play_url` 直接给 m3u8。参数：

| 用途 | 参数 |
|---|---|
| 分类表 | `ac=list` |
| 搜索 | `ac=detail&wd=<词>&pg=N` |
| 分类翻页 | `ac=detail&t=<type_id>&pg=N` |
| 详情 | `ac=detail&ids=<vod_id>` |

实测基线（2026-10-28）：49 条分类（6 父类 + 43 子类），`type_id=58`「短剧大全」12183 部；
m3u8 明文、无 `#EXT-X-KEY` 加密、分片 `video/mp2t` 首字节 `0x47`。

### 3. 这个源的真正难点：整部剧被合成一个流

暴风有一批短剧把**整部剧压成一条 m3u8**。实测《闪婚遇上裴先生2》一条播放串 =
2655 个分片 = 132.7 分钟 = 66 集。直接播就是「66 集合成 1 集、进度条两小时」。

解法是**按时间切片**：拉下整条 m3u8 → 探测总时长 → 按 120 秒切成逻辑集 →
播放时把对应区间重拼成子播放列表塞进 `media.Playlist`（`app_stream.go:188` 会直接
把它当 m3u8 响应体返回），**ExoPlayer 端零改动**。

刻意**没有用 `#EXT-X-BYTERANGE`**：这里的分片是独立 `.ts` 文件，不是单文件字节偏移，
byterange 语义对不上。

切片相关常量与实现：`bofengParseManifest`（保留分片原始行，不重新生成标签）、
`bofengRenderManifest(manifest, from, to)`、`bofengProbe`（独立 8s 超时，不跟主请求 ctx
抢时间）、`bofengSliceCuts`（含末段合并）。

### 4. `vod_play_url` 里的名字不可靠：是线路名还是集号

同一字段两种语义，实测两种都出现了：

- 《闪婚》（2022 老片）：`百度云$...#HD国语$...` —— `$` 前面是**线路名**，不是集号；
- 《闪婚遇上裴先生2》：`第01集$...` —— 是集号，但只有 1 条（整剧）。

必须分类后再决定怎么用。加了一层 `bofengRouteWords` 识别（`hd`/`bd`/`4k`/`蓝光`/`国语`/
`百度云`/`线路`/`正片` 等 40 余词），并补 `bofengMaxEpisodeNumber = 500`
—— 否则裸 `720` / `1080` 会被当成第 720 集。

用 3699 条真实条目验证：被判为线路的 5 种全部正确，被判为真集的 127 种全部正确。

### 5. 实测推翻了 4 个想当然的假设

| 假设 | 实测结论 | 处理 |
|---|---|---|
| 分类上限 40 够用 | 真实 49 条（9 父类 + 40 子类），静默截掉了 `58 短剧大全`(12183部)、`72 古装仙侠`、`74 AI漫剧`、`73 福利` | 上限改 64 |
| 父类不挂剧集，可展开后丢弃 | `ac=detail&t=58&pg=1` → `total=12183`，父类就是主力分类 | 父类先入、子类紧随，并把 `58` 提到最前 |
| 真单集 ≤ 8 分钟 | 实测《母子连心》单集 46.9 分钟、《弑流年》13.4 分钟 | 加 `bofengDeclaredTotal` 声明集数闸门 |
| 末段短是正常的 | 39 条真实整剧单流里 11 条（28%）末集 < 20 秒，最狠 2.1 秒切出 74 集 | 加 `bofengTailMin = 20s` 并入前一集 |

`58 短剧大全` 在上游 `ac=list` 里排第 38/49 位，一个 12183 部的主力分类要横向滚 38 格才看得到，
第一眼看到的是「电影片 / 动作片 / 喜剧片」。所以入父类时优先铺入 `58`，其余父类按上游顺序、子类紧随。

**关于那道声明集数闸门的实话**：39 条真实整剧单流的 `vod_remarks` **全部是「完结」**，
一条都解析不出集数（可解析比例 0/44）。所以它在当前语料上触发率为 0，等于「不设限」——
方向上安全（fail-open，宁可照切也不误拒），但别指望它真的挡住了哪部假集长片。
**真正的防线是 `bofengShortDrama`（只在 `type_id_1 == 58` 下切）和切片区间 `[3, 240]`，
这道闸门只是「上游恰好写了集数」时的兜底。**
另外 `更新至1集` 因为 `value < 2` 返回 0（不设限），真正的单集长片恰好是不设防的；
这条是刻意的取舍，改它风险大于收益。`全集36` 这种不带单位的写法同样解析不出来——
正则要求数字后必须跟「集/话/期」，放宽会把 `2024` 之类的数字也当成集数，进而误拒正当的整剧切片。

另外两处：`#EXT-X-KEY` 原来按**行数**判断轮换，同一把 key 重复写多行会误判成轮换导致不切，
改成按 `URI=` 去重（`bofengKeyURI`）；封面补齐原来会触发完整探测（单次 1.84s），
拆出 `fetchBofengCover` 轻量路径只读 `vod_pic`。

### 6. 探测失败不能塌成「1 集」（这条最容易漏）

上游在抖：同一个 URL 先 200 + `0x47`，过一会 404，之后自己恢复成 66 集。
**探测失败是这个源的常态，不是异常。**

如果探测失败就让集数 = 实际章节数，一部 66 集的短剧会被**永久写成 1 集**——
`saveDetailMetadata` 会落盘，缓存期内怎么刷新都救不回来。

改的是两层（只改 provider 不改运行时等于没改）：

1. provider 层：`probeUnknown = wantsPlaylist && !probed` 时置
   `drama.EpisodeCountUnknown = true`（`models.go` 里带 `json:"-"`，不落盘也不传前端），
   标题改成 `正片（时长未知，可下拉刷新重试）`，不谎报时长；
2. `app_runtime.go:803-813` 运行时层：`if !raw.EpisodeCountUnknown { drama.Episodes = len(chapters) }`
   —— 跳过覆盖，保留 `mergeNativeDrama` 得到的结果，下次进详情重新探测即可自愈。

**能保住什么、保不住什么，说准确**（不要以为它万能）：

| 场景 | 探测失败时的集数 | 结果 |
|---|---|---|
| 之前成功探测过（有缓存） | 上次已知集数 | ✅ 保住 |
| `vod_remarks` 能解析出集数 | 声明集数 | ✅ 保住 |
| 首次进入且备注不可解析 | 0（未知） | ⚠️ 集数未知，标题提示可下拉刷新重试 |
| 首次进入且上游返回 404/超时 | 0（未知） | ⚠️ 同上 |

也就是说抖动时不会「骗」出一个错误集数，但**首次进入碰上上游抖动，那一次就是看不到集数**，
重进详情或下拉刷新即可恢复。这是这个源的真实上限，不要宣传成「抖动也能正常看」。

顺带确认了 `0` 被当「未知」的前提安全：Go 侧 `app_catalog_cache.go:151`、Dart 侧
`lib/models.dart:219` 都对 0 做了保护，无除零风险。

### 7. 新增测试（`provider_bofeng_test.go`，20 个）

播放串解析 / 线路 vs 集号 / 诚实标题 / 整剧切片 / 短流与直播不切 / 行字段映射 /
目录分页 / 空载荷不报错 / 分类完整保留 / 末段合并 / KEY 重复不轮换 /
声明集数闸门 / 详情切片 / 声明矛盾保留完整版 / KEY 保留 / 封面零探测 /
探测失败不塌成 1 集 / 探测失败不覆盖缓存 / 裸数字 >500 非集号。

其中 `TestBofengCategoriesKeepEveryUpstreamEntry` 是这次**返工重写**的一条，值得单独说：
它原来断言的是 `len(categories) != bofengMaxCategories` —— **拿常量自己比自己**。
把上限从 64 改回 40（D1 事故原样），等式依然成立，测试照样全绿，等于这条守卫是假的。
现在改成字面量断言：条数必须 **49**、首条必须是 `58`、`58` 的 9 个子类占第 1~9 位、
末条必须是 `73`、49 个 id 不重不漏、所有子类位置都在其父类之后。

两条守卫都做了**变异自证**（把缺陷注回去，确认测试真的会红，再还原）：

| 注入的缺陷 | 测试反应 |
|---|---|
| 上限 64 → 40（D1 原样） | FAIL「上游实测 49 条，只拿到 40 条」 |
| 取消 `58` 优先，回到上游顺序 | FAIL「『短剧大全(58)』排在第 39/49 位，应当排第一」 |

`provider_bofeng.go` 1143 行，测试 718 行。`go build ./...` / `go vet ./core/` / `gofmt -l` 全 0，
20/20 PASS；全量 `go test ./core/` 的失败集合与改前基线逐字相同（0 回归）。

### 8. 定位与已知限制（说清楚，别当主力源用）

- **这是补充源，不是主力源。** 上游 `bfeng10.com` 有 4 个 IP，在抖，同一个地址几分钟内
  可能 200 → 404 → 200。已用「探测失败不塌集数」兜住，但播放稳定性不如七猫/红果。
- **电影分类的 CDN 还没通**：走 `v.fengbao14.com` / `fengbao13.com`，DNS 被劫持到 `1.1.1.1`
  且 SSL 连不上。现有掩码正则 `^v\d{1,3}\.[a-z0-9-]+\.(com|…)$` 匹配不到
  （数字在第二段），**故意没改正则**——改错会带偏鬼片那条已经跑通的掩码，等真机复测再动。
- 短剧分类下的播放已验证，**长剧/电影分类建议先按「能搜到、能播就行」验收**。
