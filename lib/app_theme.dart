import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'design_tokens.dart';

abstract final class AppTheme {
  static final light = _theme(Brightness.light);
  static final dark = _theme(Brightness.dark);

  /// TV 端只有深色一套（客厅暗光环境 + 电视系统级惯例）。
  /// 传入 'light' 也回落到深色，避免遥控器上出现一块刺眼的浅色面板。
  static ThemeMode mode(String preference, {bool television = false}) {
    if (television) return ThemeMode.dark;
    return switch (preference) {
      'light' => ThemeMode.light,
      'dark' => ThemeMode.dark,
      _ => ThemeMode.system,
    };
  }

  static String label(String preference) => switch (preference) {
    'light' => '浅色',
    'dark' => '深色',
    _ => '跟随系统',
  };

  static SystemUiOverlayStyle systemBars(Brightness brightness) {
    final icons = brightness == Brightness.dark
        ? Brightness.light
        : Brightness.dark;
    return SystemUiOverlayStyle(
      statusBarColor: Colors.transparent,
      statusBarIconBrightness: icons,
      statusBarBrightness: brightness,
      systemStatusBarContrastEnforced: false,
      systemNavigationBarColor: Colors.transparent,
      systemNavigationBarDividerColor: Colors.transparent,
      systemNavigationBarIconBrightness: icons,
      systemNavigationBarContrastEnforced: false,
    );
  }

  static ThemeData _theme(Brightness brightness) {
    final dark = brightness == Brightness.dark;
    final background = dark ? TvColor.canvas : const Color(0xFFFAF8F6);
    final scheme = ColorScheme.fromSeed(
      seedColor: TvColor.brand,
      brightness: brightness,
      primary: dark ? TvColor.brand : const Color(0xFFB03A21),
      onPrimary: dark ? TvColor.onBrand : Colors.white,
      primaryContainer: dark ? TvColor.brandDim : const Color(0xFFFFE0D8),
      onPrimaryContainer: dark ? TvColor.brand : const Color(0xFF4B160D),
      tertiary: dark ? TvColor.rating : const Color(0xFF805500),
      surface: dark ? TvColor.surface : Colors.white,
      onSurface: dark ? TvColor.text : const Color(0xFF252328),
      onSurfaceVariant: dark ? TvColor.text2 : const Color(0xFF68636C),
      outline: dark ? TvColor.text3 : const Color(0xFF7C757D),
      outlineVariant: dark ? TvColor.surface3 : const Color(0xFFE3DFDC),
      surfaceContainerLowest: dark ? TvColor.canvas : Colors.white,
      surfaceContainerLow: dark ? TvColor.canvas2 : const Color(0xFFF7F3F0),
      surfaceContainer: dark ? TvColor.surface : const Color(0xFFF2EEEB),
      surfaceContainerHigh: dark ? TvColor.surface2 : const Color(0xFFEDE8E5),
      surfaceContainerHighest: dark ? TvColor.surface3 : const Color(0xFFE6E0DC),
      surfaceTint: Colors.transparent,
    );
    final base = ThemeData(useMaterial3: true, colorScheme: scheme);
    return base.copyWith(
      scaffoldBackgroundColor: background,
      canvasColor: background,
      textTheme: _textTheme(base.textTheme),
      appBarTheme: AppBarTheme(
        backgroundColor: dark ? Colors.transparent : background,
        foregroundColor: scheme.onSurface,
        scrolledUnderElevation: 0,
        elevation: 0,
        centerTitle: false,
        systemOverlayStyle: systemBars(brightness),
      ),
      dividerTheme: DividerThemeData(
        color: dark ? TvColor.hair : scheme.outlineVariant,
        thickness: 1,
        space: 1,
      ),
      navigationRailTheme: NavigationRailThemeData(
        backgroundColor: Colors.transparent,
        useIndicator: false,
        selectedIconTheme: IconThemeData(color: scheme.primary),
        unselectedIconTheme: IconThemeData(color: scheme.onSurfaceVariant),
        selectedLabelTextStyle: TextStyle(
          color: scheme.primary,
          fontWeight: FontWeight.w700,
        ),
        unselectedLabelTextStyle: TextStyle(color: scheme.onSurfaceVariant),
      ),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: dark ? TvColor.surface : scheme.surfaceContainer,
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(TvSpace.cardRadius),
          borderSide: BorderSide.none,
        ),
      ),
      // 10-foot 视距下默认焦点 overlay 几乎不可辨 → 用品牌暗底兜底。
      focusColor: scheme.primaryContainer,
      splashFactory: NoSplash.splashFactory,
    );
  }

  /// 字阶：层高靠字重与明度拉开，中文不做负字距。
  /// 注意：此处**只调字重与行高，不动字号**——TV 各家 density 不一（960dp / 1920dp
  /// 都有），硬改 fontSize 会在某类机型上突然变小。字号仍由各组件显式给值。
  static TextTheme _textTheme(TextTheme base) => base.copyWith(
    displaySmall: base.displaySmall?.copyWith(
      fontWeight: FontWeight.w800,
      height: 1.12,
    ),
    headlineMedium: base.headlineMedium?.copyWith(
      fontWeight: FontWeight.w800,
      height: 1.16,
    ),
    titleLarge: base.titleLarge?.copyWith(
      fontWeight: FontWeight.w700,
      height: 1.2,
    ),
    titleMedium: base.titleMedium?.copyWith(
      fontWeight: FontWeight.w800,
      height: 1.1,
    ),
    bodyMedium: base.bodyMedium?.copyWith(height: 1.5),
    bodySmall: base.bodySmall?.copyWith(height: 1.4),
    labelLarge: base.labelLarge?.copyWith(fontWeight: FontWeight.w700),
  );
}
