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
