# Android TV 适配改造说明

基线：上游 guoapp 0.2.64+71（zip 快照，无 git）。本次改造目标：独立 TV 版安卓应用（`com.duanju.duanju_app.tv`），修复全部 P0 级电视不可用问题。

## 一、独立 TV 版（flavor `tv`）

| 项 | 手机版（flavor `phone`） | 电视版（flavor `tv`） |
| --- | --- | --- |
| 包名 | `com.duanju.duanju_app` | `com.duanju.duanju_app.tv` |
| 应用名 | 红果鉴 / 真果鉴 | 红果鉴 TV / 真果鉴 TV |
| versionName | 0.2.64 | 0.2.64-tv |
| 桌面入口 | 手机桌面 | 仅电视桌面（LEANBACK_LAUNCHER） |
| 数据 | 独立 | 独立（可两版并存） |

改动文件：

- `android/app/build.gradle.kts`：新增 `flavorDimensions platform` + `phone`/`tv` flavor。**引入 flavor 后必须始终 `--flavor` 构建**，裸 `flutter build apk` 会因 Gradle task 缺失失败。
- `android/app/src/tv/AndroidManifest.xml`：用 `tools:node="replace"` 替换 MAIN intent-filter，只保留 LEANBACK_LAUNCHER；banner/label 走 manifestPlaceholders。
- `lib/app_build.dart`：新增 `TV_BUILD` dart-define，`appName`/`appSlug` 自动带 TV 后缀。
- `scripts/app_build.py`：`BuildVariant` 增加 `flavor` 字段；`flutter_arguments` 始终携带 `--flavor`；tv 额外注入 `--dart-define=TV_BUILD=true`；产物 slug 自动变 `hongguojian-tv-x.y.z`。
- `scripts/build_android.py`、`scripts/build_native.py`、`scripts/package_release.py`：消费 `--flavor` 参数。

构建命令：

```sh
python scripts/build_android.py --flavor tv              # 红果鉴 TV
python scripts/build_android.py --flavor tv --all-sources  # 真果鉴 TV
python scripts/build_android.py                          # 手机版（默认 phone，行为不变）
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
