import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'design_tokens.dart';

export 'app_build.dart';

// 版本名由构建脚本按 flavor 注入：电视端走 versions.json（1.0.x），
// 手机端沿用 pubspec.yaml。这里只是兜底值，不要把版本号写死进代码。
const appVersion = String.fromEnvironment('APP_VERSION', defaultValue: '0.2.69');

DateTime? _lastExitIntentAt;
ScaffoldFeatureController<SnackBar, SnackBarClosedReason>? _lastExitSnackBar;

/// 电视上的退出确认：2 秒内第二次按返回才真正退出，否则显示提示（防误触）。
/// 使用 SnackBar 而非对话框——对话框是路由，会被后续返回事件意外关闭。
///
/// 注意：返回键可能被框架层与原生通道层双重投递（TCL 等电视上同一次按键
/// 会触发两次），因此 600ms 内的第二次调用视为同一次按键的重复投递，
/// 直接忽略，否则会跳过提示直接退出。
void requestExitConfirmation(BuildContext context) {
  final now = DateTime.now();
  final last = _lastExitIntentAt;
  if (last != null) {
    final delta = now.difference(last);
    if (delta < const Duration(milliseconds: 600)) {
      debugPrint(
        'BackTrace: exitIntent duplicate ignored (+${delta.inMilliseconds}ms)',
      );
      return;
    }
    if (delta < const Duration(seconds: 2)) {
      debugPrint('BackTrace: exit confirmed (+${delta.inMilliseconds}ms)');
      SystemNavigator.pop();
      return;
    }
  }
  _lastExitIntentAt = now;
  debugPrint('BackTrace: exit prompt shown');
  final messenger = ScaffoldMessenger.maybeOf(context);
  messenger?.hideCurrentSnackBar();
  final controller = messenger?.showSnackBar(
    const SnackBar(
      content: Text('再按一次返回键退出应用'),
      duration: Duration(seconds: 2),
    ),
  );
  _lastExitSnackBar = controller;
  controller?.closed.whenComplete(() {
    if (identical(controller, _lastExitSnackBar)) _lastExitSnackBar = null;
  });
}

ThemeData televisionTheme(ThemeData theme) {
  final colors = theme.colorScheme;
  final dark = theme.brightness == Brightness.dark;
  // 焦点语言：获焦整块反白（深色主题下白底黑字），3m 外辨识度最高。
  // 不做描边高亮——描边在客厅视距下几乎无效。
  final focusSide = WidgetStateProperty.resolveWith<BorderSide?>(
    (states) => states.contains(WidgetState.focused)
        ? BorderSide(color: dark ? TvColor.focusRing : colors.primary, width: 3)
        : BorderSide.none,
  );
  final focusBackground = WidgetStateProperty.resolveWith<Color?>(
    (states) => states.contains(WidgetState.focused)
        ? (dark ? TvColor.focusRing : colors.primaryContainer)
        : null,
  );
  final focusForeground = WidgetStateProperty.resolveWith<Color?>(
    (states) => states.contains(WidgetState.focused)
        ? (dark ? const Color(0xFF0B0C10) : colors.onSurface)
        : null,
  );
  final button = ButtonStyle(
    side: focusSide,
    backgroundColor: focusBackground,
    foregroundColor: focusForeground,
    shape: const WidgetStatePropertyAll(StadiumBorder()),
    minimumSize: const WidgetStatePropertyAll(Size(56, 50)),
    textStyle: const WidgetStatePropertyAll(
      TextStyle(fontSize: 17, fontWeight: FontWeight.w600),
    ),
  );
  // 10-foot 视距下默认焦点 overlay 几乎不可辨：
  // Flutter 3.47 的 ListTileThemeData 已无 focusColor/hoverColor/overlayColor，
  // 焦点高亮统一由下方 ThemeData.focusColor 全局兜底
  return theme.copyWith(
    focusColor: dark ? TvColor.brandDim : colors.primaryContainer,
    // 电视上没有触屏，水波纹只会干扰遥控器焦点反馈；手机端必须保留。
    splashFactory: NoSplash.splashFactory,
    radioTheme: RadioThemeData(fillColor: WidgetStateProperty.resolveWith(
      (states) => states.contains(WidgetState.focused)
          ? colors.primary
          : null,
    )),
    checkboxTheme: CheckboxThemeData(
      side: BorderSide(color: colors.primary, width: 2),
    ),
    switchTheme: SwitchThemeData(
      thumbColor: WidgetStateProperty.resolveWith(
        (states) => states.contains(WidgetState.focused)
            ? colors.primary
            : null,
      ),
      trackOutlineColor: WidgetStateProperty.resolveWith(
        (states) => states.contains(WidgetState.focused)
            ? colors.primary
            : null,
      ),
    ),
    chipTheme: theme.chipTheme.copyWith(
      labelStyle: theme.chipTheme.labelStyle?.copyWith(fontSize: 17),
      side: BorderSide(color: colors.primary, width: 2),
    ),
    popupMenuTheme: theme.popupMenuTheme.copyWith(
      color: dark ? TvColor.surface : colors.surface,
      surfaceTintColor: Colors.transparent,
      elevation: 16,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(TvSpace.cardRadius),
        side: BorderSide(color: TvColor.hair),
      ),
    ),
    dialogTheme: theme.dialogTheme.copyWith(
      backgroundColor: dark ? TvColor.surface : null,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(TvSpace.cardRadius),
      ),
      titleTextStyle: (theme.dialogTheme.titleTextStyle ??
              theme.textTheme.titleLarge)
          ?.copyWith(fontSize: 26),
      contentTextStyle: theme.textTheme.bodyLarge,
    ),
    iconButtonTheme: IconButtonThemeData(
      style: button.copyWith(backgroundColor: focusBackground),
    ),
    filledButtonTheme: FilledButtonThemeData(style: button),
    outlinedButtonTheme: OutlinedButtonThemeData(style: button),
    textButtonTheme: TextButtonThemeData(
      style: button.copyWith(backgroundColor: focusBackground),
    ),
    inputDecorationTheme: theme.inputDecorationTheme.copyWith(
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(TvSpace.cardRadius),
        borderSide: BorderSide(color: colors.primary, width: 3),
      ),
    ),
  );
}

class AppDevice {
  const AppDevice({this.television = false, this.version = appVersion});
  final bool television;
  final String version;
  /// 最近一次设备检测结果（main 启动与恢复时刷新）。
  /// 供无法访问 BuildContext 的初始化逻辑（如播放器创建）同步判断电视设备。
  static bool isTv = false;
  static const channel = MethodChannel('duanju/device');

  static Future<AppDevice> detect({
    AppDevice fallback = const AppDevice(),
  }) async {
    if (defaultTargetPlatform != TargetPlatform.android) {
      return const AppDevice();
    }
    try {
      final data = await channel
          .invokeMapMethod<String, dynamic>('deviceInfo')
          .timeout(const Duration(seconds: 2));
      final television = data?['television'];
      if (television is! bool) return fallback;
      final version = data?['version'];
      return AppDevice(
        television: television,
        version: version is String && version.isNotEmpty
            ? version
            : fallback.version,
      );
    } on PlatformException {
      return fallback;
    } on MissingPluginException {
      return fallback;
    } on TimeoutException {
      return fallback;
    }
  }

  static Future<double> getBrightness() async {
    if (defaultTargetPlatform != TargetPlatform.android) return 0.5;
    try {
      final value = await channel.invokeMethod<double>('getBrightness');
      return (value ?? 0.5).clamp(0.0, 1.0);
    } catch (_) {
      return 0.5;
    }
  }

  static Future<void> setBrightness(double brightness) async {
    if (defaultTargetPlatform != TargetPlatform.android) return;
    try {
      await channel.invokeMethod('setBrightness', {'brightness': brightness.clamp(0.01, 1.0)});
    } catch (_) {}
  }

  static Future<void> resetBrightness() async {
    if (defaultTargetPlatform != TargetPlatform.android) return;
    try {
      await channel.invokeMethod('resetBrightness');
    } catch (_) {}
  }

  static Future<void> setKeepScreenOn(bool enabled) async {
    if (defaultTargetPlatform != TargetPlatform.android) return;
    try {
      await channel.invokeMethod('setKeepScreenOn', {'enabled': enabled});
    } catch (_) {}
  }
}

class AppLayout extends InheritedWidget {
  const AppLayout({
    super.key,
    required this.television,
    this.version = appVersion,
    required super.child,
  });
  final bool television;
  final String version;

  static bool isTelevision(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<AppLayout>()?.television ??
      false;
  static String versionOf(BuildContext context) =>
      context.dependOnInheritedWidgetOfExactType<AppLayout>()?.version ??
      appVersion;

  @override
  bool updateShouldNotify(AppLayout oldWidget) =>
      television != oldWidget.television || version != oldWidget.version;
}
