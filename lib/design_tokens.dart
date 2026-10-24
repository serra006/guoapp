import 'package:flutter/material.dart';

/// TV 端设计契约 v1 的实现层 token。
/// 规范来源：`design/design-contract.md`（对应原型 `design/tv-redesign-v1.html`）。
///
/// 全站唯一的强调色是珊瑚橙 [brand]，其余用中性灰阶拉开层级；
/// 焦点语言全站统一为「白环 3px + 提亮 + 轻微放大」，保证 3m 视距可辨。
abstract final class TvColor {
  /// 画布底：比旧版 #101114 更深，把亮度预算让给海报。
  static const canvas = Color(0xFF08090C);

  /// Hero 下方过渡渐层。
  static const canvas2 = Color(0xFF0D0F14);

  /// 面板、选集容器。
  static const surface = Color(0xFF14161D);

  /// 缩略图兜底底。
  static const surface2 = Color(0xFF1B1E27);

  /// 分隔、未激活块。
  static const surface3 = Color(0xFF242833);

  /// 品牌主色：CTA、进度、选中、TOP 标记。
  static const brand = Color(0xFFFF6B4A);

  /// 导航选中底色（brand @ 16%）。
  static const brandDim = Color(0x29FF6B4A);

  /// 焦点卡片外发光（brand @ 45%）。
  static const brandGlow = Color(0x73FF6B4A);

  /// 主文字 17.6:1。
  static const text = Color(0xFFF6F7F9);

  /// 次文字 8.4:1。
  static const text2 = Color(0xFFA9AFBE);

  /// 弱文字 4.6:1。
  static const text3 = Color(0xFF6B7285);

  /// 发丝线（白 @ 9%）。
  static const hair = Color(0x17FFFFFF);

  /// 焦点环：全站唯一焦点语言。
  static const focusRing = Colors.white;

  /// 评分金。
  static const rating = Color(0xFFFFC861);

  /// 主 CTA 上的文字色（珊瑚底配深字，对比更稳）。
  static const onBrand = Color(0xFF1A0A06);
}

abstract final class TvSpace {
  /// 焦点环宽度。
  static const ringWidth = 3.0;

  /// 卡片焦点放大倍数。
  static const focusScale = 1.065;

  /// 按钮焦点放大倍数。
  static const buttonFocusScale = 1.045;

  /// 卡片圆角。
  static const cardRadius = 12.0;

  /// 按钮、徽标的 pill 圆角。
  static const pill = 999.0;

  /// overscan 安全区（1080p 电视普遍 3–5% 裁边）。
  static const safeTop = 34.0;
  static const safeBottom = 56.0;
  static const safeSide = 72.0;

  /// 横版缩略图宽度与比例。
  static const cardWidth = 300.0;
  static const cardAspect = 16 / 9;

  /// 竖版海报比例（手机端与非首页网格仍沿用）。
  static const posterAspect = 2 / 3;

  /// 非焦点内容的降亮比例。
  static const idleDim = .70;
}

abstract final class TvText {
  static const hero = 30.0;
  static const h1 = 28.0;
  static const h2 = 16.0;
  static const osd = 20.0;
  static const cardTitle = 11.0;
  static const body = 9.5;
  static const meta = 9.0;
  static const caption = 8.0;
  static const nav = 9.0;
}

/// 焦点三重可辨所需的焦点装饰：白环 + 白晕 + 珊瑚外发光。
List<BoxShadow> tvFocusShadows({bool brand = true}) => [
  if (brand)
    BoxShadow(
      color: TvColor.brandGlow,
      blurRadius: 34,
      spreadRadius: 2,
    ),
  const BoxShadow(
    color: Color(0x24000000),
    blurRadius: 64,
    offset: Offset(0, 30),
    spreadRadius: -16,
  ),
];

/// 把任意单色图标/文字整体反相（白→黑），用于「获焦整体反白」的按钮。
/// 卡片海报不做反相，只做提亮。
const ColorFilter tvInvertFilter = ColorFilter.matrix(<double>[
  -1, 0, 0, 0, 255, //
  0, -1, 0, 0, 255, //
  0, 0, -1, 0, 255, //
  0, 0, 0, 1, 0, //
]);

/// 非焦点卡片的降亮降饱和（保持发光，比 Opacity 更接近设计稿）。
ColorFilter dimmedFilter({double brightness = TvSpace.idleDim}) {
  final b = brightness;
  const s = .86;
  // 亮度系数 + 饱和度的近似合成矩阵。
  return ColorFilter.matrix(<double>[
    0.213 * (1 - s) * b + s * b, 0.715 * (1 - s) * b, 0.072 * (1 - s) * b, 0, 0, //
    0.213 * (1 - s) * b, 0.715 * (1 - s) * b + s * b, 0.072 * (1 - s) * b, 0, 0, //
    0.213 * (1 - s) * b, 0.715 * (1 - s) * b, 0.072 * (1 - s) * b + s * b, 0, 0, //
    0, 0, 0, 1, 0, //
  ]);
}

/// 焦点提亮（brightness 1.1）。
const ColorFilter tvBrightenFilter = ColorFilter.matrix(<double>[
  1.1, 0, 0, 0, 0, //
  0, 1.1, 0, 0, 0, //
  0, 0, 1.1, 0, 0, //
  0, 0, 0, 1, 0, //
]);
