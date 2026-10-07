import 'dart:async';
import 'package:flutter/material.dart';

import 'search_input.dart';
import 'package:flutter/services.dart';

class RemoteTarget extends StatefulWidget {
  const RemoteTarget({
    super.key,
    required this.child,
    required this.onPressed,
    this.focusNode,
    this.onFocus,
    this.autofocus = false,
    this.selected = false,
    this.label,
    this.radius = 14,
    this.padding = const EdgeInsets.all(4),
    this.borderWidth = 3,
    this.outlined = false,
  });
  final Widget child;
  final VoidCallback? onPressed;
  final FocusNode? focusNode;
  final VoidCallback? onFocus;
  final bool autofocus;
  final bool selected;
  final String? label;
  final double radius;
  final EdgeInsetsGeometry padding;
  final double borderWidth;
  final bool outlined;

  @override
  State<RemoteTarget> createState() => _RemoteTargetState();
}

class _RemoteTargetState extends State<RemoteTarget> {
  bool _focused = false;

  @override
  Widget build(BuildContext context) => FocusableActionDetector(
    focusNode: widget.focusNode,
    autofocus: widget.autofocus,
    enabled: widget.onPressed != null,
    actions: {
      ActivateIntent: CallbackAction<ActivateIntent>(
        onInvoke: (_) {
          widget.onPressed?.call();
          return null;
        },
      ),
    },
    onFocusChange: (focused) {
      if (mounted) {
        setState(() => _focused = focused);
      }
      if (focused) {
        widget.onFocus?.call();
        WidgetsBinding.instance.addPostFrameCallback((_) {
          if (mounted && _focused) {
            Scrollable.ensureVisible(
              context,
              alignmentPolicy: ScrollPositionAlignmentPolicy.keepVisibleAtEnd,
            );
          }
        });
      }
    },
    child: Semantics(
      button: true,
      enabled: widget.onPressed != null,
      focused: _focused,
      label: widget.label,
      child: MouseRegion(
        cursor: widget.onPressed == null
            ? SystemMouseCursors.basic
            : SystemMouseCursors.click,
        child: GestureDetector(
          behavior: HitTestBehavior.opaque,
          onTap: widget.onPressed,
          child: AnimatedContainer(
            duration: const Duration(milliseconds: 120),
            padding: widget.padding,
            decoration: BoxDecoration(
              color: widget.selected
                  ? Theme.of(context).colorScheme.primaryContainer
                  : _focused && widget.outlined
                  ? Theme.of(context).colorScheme.surfaceContainerHighest
                  : _focused
                  ? Theme.of(context).colorScheme.primaryContainer
                  : Colors.transparent,
              borderRadius: BorderRadius.circular(widget.radius),
              border: Border.all(
                color: _focused
                    ? Theme.of(context).colorScheme.primary
                    : widget.selected
                    ? Theme.of(context).colorScheme.primary
                    : widget.outlined
                    ? Theme.of(context).colorScheme.outlineVariant
                    : Colors.transparent,
                width: widget.borderWidth,
              ),
            ),
            child: ExcludeFocus(child: widget.child),
          ),
        ),
      ),
    ),
  );
}

class RemoteButton extends StatelessWidget {
  const RemoteButton({
    super.key,
    required this.label,
    required this.onPressed,
    this.icon,
    this.selected = false,
    this.autofocus = false,
    this.focusNode,
  });
  final String label;
  final VoidCallback? onPressed;
  final IconData? icon;
  final bool selected;
  final bool autofocus;
  final FocusNode? focusNode;

  @override
  Widget build(BuildContext context) => RemoteTarget(
    onPressed: onPressed,
    selected: selected,
    autofocus: autofocus,
    focusNode: focusNode,
    label: label,
    child: Padding(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (icon != null) ...[Icon(icon, size: 22), const SizedBox(width: 8)],
          Text(
            label,
            style: TextStyle(
              fontSize: 17,
              color: onPressed == null
                  ? Theme.of(context).disabledColor
                  : Theme.of(context).colorScheme.onSurface,
            ),
          ),
        ],
      ),
    ),
  );
}

class RemoteGrid extends StatefulWidget {
  const RemoteGrid({
    super.key,
    required this.itemKeys,
    required this.columns,
    required this.itemExtent,
    required this.itemBuilder,
    this.controller,
    this.spacing = 14,
    this.padding = const EdgeInsets.all(18),
    this.footer,
    this.initialIndex = 0,
    this.autofocus = false,
  });
  final List<String> itemKeys;
  final int columns;
  final double itemExtent;
  final double spacing;
  final EdgeInsets padding;
  final ScrollController? controller;
  final Widget? footer;
  final int initialIndex;
  final bool autofocus;
  final Widget Function(BuildContext, int, FocusNode, VoidCallback) itemBuilder;

  @override
  State<RemoteGrid> createState() => _RemoteGridState();
}

class _RemoteGridState extends State<RemoteGrid> {
  final _ownScroll = ScrollController();
  final _nodes = <String, FocusNode>{};
  String? _focused;
  String? _target;
  int _generation = 0;
  ScrollController get _scroll => widget.controller ?? _ownScroll;

  FocusNode _node(int index) => _nodes.putIfAbsent(
    widget.itemKeys[index],
    () => FocusNode(debugLabel: 'remote-${widget.itemKeys[index]}'),
  );

  @override
  void initState() {
    super.initState();
    if (widget.autofocus) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted && widget.itemKeys.isNotEmpty) {
          _focusAt(widget.initialIndex.clamp(0, widget.itemKeys.length - 1));
        }
      });
    }
  }

  void _focusAt(int index) {
    final generation = ++_generation;
    _target = widget.itemKeys[index];
    final node = _node(index);

    if (_scroll.hasClients) {
      final row = index ~/ widget.columns;
      final itemTop =
          widget.padding.top + row * (widget.itemExtent + widget.spacing);
      final itemBottom = itemTop + widget.itemExtent;
      final currentOffset = _scroll.offset;
      final viewportHeight = _scroll.position.viewportDimension;

      double? targetOffset;
      if (row == 0) {
        if (currentOffset > 0.0) {
          targetOffset = 0.0;
        }
      } else if (itemTop < currentOffset + widget.padding.top) {
        targetOffset = itemTop - widget.padding.top;
      } else if (itemBottom > currentOffset + viewportHeight - widget.padding.bottom) {
        targetOffset = itemBottom - viewportHeight + widget.padding.bottom;
      }

      if (targetOffset != null) {
        final clamped = targetOffset.clamp(0.0, _scroll.position.maxScrollExtent);
        if ((clamped - currentOffset).abs() > 1.0) {
          _scroll.animateTo(
            clamped,
            duration: const Duration(milliseconds: 180),
            curve: Curves.easeOutCubic,
          );
        }
      }
    }

    if (node.context != null) {
      node.requestFocus();
      _target = null;
    } else {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted && generation == _generation && node.context != null) {
          node.requestFocus();
          _target = null;
        }
      });
    }
  }

  KeyEventResult _key(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent && event is! KeyRepeatEvent) {
      return KeyEventResult.ignored;
    }
    if (_focused == null || !(_nodes[_focused]?.hasFocus ?? false)) {
      return KeyEventResult.ignored;
    }
    final index = widget.itemKeys.indexOf(_target ?? _focused!);
    if (index < 0) return KeyEventResult.ignored;
    final key = event.logicalKey;
    final int next;
    if (key == LogicalKeyboardKey.arrowDown) {
      final lastRow = (widget.itemKeys.length - 1) ~/ widget.columns;
      if (index ~/ widget.columns == lastRow) {
        if (_scroll.hasClients &&
            _scroll.offset < _scroll.position.maxScrollExtent - 1.0) {
          _scroll.animateTo(
            _scroll.position.maxScrollExtent,
            duration: const Duration(milliseconds: 150),
            curve: Curves.easeOutCubic,
          );
          return KeyEventResult.handled;
        }
        return KeyEventResult.ignored;
      }
      next = (index + widget.columns).clamp(0, widget.itemKeys.length - 1);
    } else if (key == LogicalKeyboardKey.arrowUp) {
      if (index < widget.columns) {
        if (_scroll.hasClients && _scroll.offset > 1.0) {
          _scroll.animateTo(
            0.0,
            duration: const Duration(milliseconds: 150),
            curve: Curves.easeOutCubic,
          );
          return KeyEventResult.handled;
        }
        FocusScope.of(context).focusInDirection(TraversalDirection.up);
        return KeyEventResult.handled;
      }
      next = index - widget.columns;
    } else if (key == LogicalKeyboardKey.arrowRight) {
      if (index % widget.columns == widget.columns - 1 ||
          index == widget.itemKeys.length - 1) {
        return KeyEventResult.handled;
      }
      next = index + 1;
    } else if (key == LogicalKeyboardKey.arrowLeft) {
      if (index % widget.columns == 0) return KeyEventResult.ignored;
      next = index - 1;
    } else {
      return KeyEventResult.ignored;
    }
    if (next < 0 || next >= widget.itemKeys.length) {
      return KeyEventResult.ignored;
    }
    _focusAt(next);
    return KeyEventResult.handled;
  }

  @override
  void didUpdateWidget(covariant RemoteGrid oldWidget) {
    super.didUpdateWidget(oldWidget);
    final removed = _nodes.keys
        .where((key) => !widget.itemKeys.contains(key))
        .toList();
    final restore = removed.any((key) => _nodes[key]!.hasFocus);
    final index = oldWidget.itemKeys.indexOf(_focused ?? '');
    for (final key in removed) {
      _nodes.remove(key)?.dispose();
    }
    if (_focused != null && !widget.itemKeys.contains(_focused)) {
      _focused = null;
      _target = null;
      _generation++;
    }
    if (restore && widget.itemKeys.isNotEmpty) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted && widget.itemKeys.isNotEmpty) {
          _focusAt(index.clamp(0, widget.itemKeys.length - 1));
        }
      });
    }
  }

  @override
  void dispose() {
    _generation++;
    for (final node in _nodes.values) {
      node.dispose();
    }
    _ownScroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => Focus(
    canRequestFocus: false,
    skipTraversal: true,
    onKeyEvent: _key,
    child: CustomScrollView(
      controller: _scroll,
      slivers: [
        SliverPadding(
          padding: widget.padding,
          sliver: SliverGrid(
            gridDelegate: SliverGridDelegateWithFixedCrossAxisCount(
              crossAxisCount: widget.columns,
              mainAxisExtent: widget.itemExtent,
              crossAxisSpacing: widget.spacing,
              mainAxisSpacing: widget.spacing,
            ),
            delegate: SliverChildBuilderDelegate(
              (context, index) =>
                  widget.itemBuilder(context, index, _node(index), () {
                    _focused = widget.itemKeys[index];
                  }),
              childCount: widget.itemKeys.length,
            ),
          ),
        ),
        if (widget.footer != null) SliverToBoxAdapter(child: widget.footer),
      ],
    ),
  );
}

class RemoteEpisodeButton extends StatelessWidget {
  const RemoteEpisodeButton({
    super.key,
    required this.number,
    required this.onPressed,
    this.vip = false,
    this.current = false,
    this.compact = false,
    this.focusNode,
    this.onFocus,
  });
  final int number;
  final bool vip;
  final bool current;
  final bool compact;
  final VoidCallback onPressed;
  final FocusNode? focusNode;
  final VoidCallback? onFocus;

  @override
  Widget build(BuildContext context) => RemoteTarget(
    focusNode: focusNode,
    onFocus: onFocus,
    selected: current,
    radius: compact ? 8 : 14,
    padding: compact ? const EdgeInsets.all(2) : const EdgeInsets.all(4),
    borderWidth: compact ? 1.2 : 2,
    outlined: true,
    onPressed: onPressed,
    label: '第 $number 集${vip ? '，VIP 试看' : ''}',
    child: Center(
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Flexible(
            child: Text(
              '$number',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: compact ? 14 : 20,
                fontWeight: current ? FontWeight.w700 : FontWeight.w500,
              ),
            ),
          ),
          if (vip) ...[
            SizedBox(width: compact ? 2 : 4),
            Icon(
              Icons.workspace_premium_rounded,
              color: Theme.of(context).colorScheme.tertiary,
              size: compact ? 15 : 18,
            ),
          ],
        ],
      ),
    ),
  );
}

class TelevisionSearchDialog extends StatefulWidget {
  const TelevisionSearchDialog({
    super.key,
    required this.initialValue,
    required this.title,
    this.suggestions,
    this.recentSearches = const [],
    this.onCancel,
  });
  final Future<List<String>> Function(String)? suggestions;
  final List<String> recentSearches;
  final VoidCallback? onCancel;
  final String initialValue;
  final String title;

  @override
  State<TelevisionSearchDialog> createState() => _TelevisionSearchDialogState();
}

class _TelevisionSearchDialogState extends State<TelevisionSearchDialog> {
  late String _query = widget.initialValue;
  List<String> _results = [];
  bool _searching = false;
  Timer? _debounce;

  static const _keys = [
    'A', 'B', 'C', 'D', 'E', 'F',
    'G', 'H', 'I', 'J', 'K', 'L',
    'M', 'N', 'O', 'P', 'Q', 'R',
    'S', 'T', 'U', 'V', 'W', 'X',
    'Y', 'Z', '1', '2', '3', '4',
    '5', '6', '7', '8', '9', '0',
  ];

  @override
  void initState() {
    super.initState();
    if (_query.isNotEmpty) {
      _fetchSuggestions(_query);
    }
  }

  @override
  void dispose() {
    _debounce?.cancel();
    super.dispose();
  }

  void _onKeyPress(String char) {
    setState(() => _query += char);
    _onQueryChanged();
  }

  void _onBackspace() {
    if (_query.isNotEmpty) {
      setState(() => _query = _query.substring(0, _query.length - 1));
      _onQueryChanged();
    }
  }

  void _onClear() {
    if (_query.isNotEmpty) {
      setState(() {
        _query = '';
        _results = [];
      });
      _debounce?.cancel();
    }
  }

  void _onQueryChanged() {
    _debounce?.cancel();
    final trimmed = _query.trim();
    if (trimmed.isEmpty) {
      setState(() => _results = []);
      return;
    }
    _debounce = Timer(const Duration(milliseconds: 250), () {
      _fetchSuggestions(trimmed);
    });
  }

  Future<void> _fetchSuggestions(String text) async {
    if (widget.suggestions == null) return;
    setState(() => _searching = true);
    try {
      final list = await widget.suggestions!(text);
      if (mounted) {
        setState(() {
          _results = list;
          _searching = false;
        });
      }
    } catch (_) {
      if (mounted) setState(() => _searching = false);
    }
  }

  void _submit(String text) {
    var target = text.trim();
    if (target.isEmpty) return;
    // 若用户输入纯拼音头字母且已成功联想出短剧，直接点击搜索时优先取第一部短剧全名
    if (_results.isNotEmpty && RegExp(r'^[a-zA-Z0-9]+$').hasMatch(target)) {
      target = _results.first;
    }
    Navigator.pop(context, target);
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return AlertDialog(
      titlePadding: const EdgeInsets.fromLTRB(24, 20, 24, 12),
      contentPadding: const EdgeInsets.fromLTRB(24, 0, 24, 16),
      title: Row(
        children: [
          Icon(Icons.tv_rounded, color: theme.colorScheme.primary),
          const SizedBox(width: 10),
          Expanded(child: Text(widget.title, style: const TextStyle(fontSize: 20))),
          IconButton(
            tooltip: '关闭',
            icon: const Icon(Icons.close_rounded),
            onPressed: () => Navigator.pop(context),
          ),
        ],
      ),
      content: SizedBox(
        width: 820,
        height: 520,
        child: Column(
          children: [
            // 顶部只读搜索显示条，彻底杜绝系统软键盘弹出
            Container(
              height: 56,
              padding: const EdgeInsets.symmetric(horizontal: 16),
              decoration: BoxDecoration(
                color: theme.colorScheme.surfaceContainerHighest.withValues(alpha: .5),
                borderRadius: BorderRadius.circular(14),
                border: Border.all(color: theme.colorScheme.primary.withValues(alpha: .3)),
              ),
              child: Row(
                children: [
                  Icon(Icons.search_rounded, color: theme.colorScheme.primary, size: 28),
                  const SizedBox(width: 12),
                  Expanded(
                    child: Text(
                      _query.isEmpty ? '按遥控器输入剧名拼音头字母 (如: BFLC)' : _query,
                      style: TextStyle(
                        fontSize: 20,
                        fontWeight: _query.isEmpty ? FontWeight.normal : FontWeight.bold,
                        color: _query.isEmpty
                            ? theme.hintColor
                            : theme.colorScheme.onSurface,
                        letterSpacing: _query.isEmpty ? 0 : 2,
                      ),
                    ),
                  ),
                  if (_query.isNotEmpty) ...[
                    Text('${_query.length} 字母', style: TextStyle(color: theme.hintColor, fontSize: 14)),
                    const SizedBox(width: 8),
                  ],
                ],
              ),
            ),
            const SizedBox(height: 16),
            // 主体区域：左侧爱奇艺式字母键盘，右侧智能联想与历史
            Expanded(
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  // 左侧全字母/数字虚拟键盘
                  SizedBox(
                    width: 440,
                    child: Column(
                      children: [
                        Expanded(
                          child: GridView.builder(
                            physics: const NeverScrollableScrollPhysics(),
                            gridDelegate: const SliverGridDelegateWithFixedCrossAxisCount(
                              crossAxisCount: 6,
                              mainAxisSpacing: 8,
                              crossAxisSpacing: 8,
                              childAspectRatio: 1.4,
                            ),
                            itemCount: _keys.length,
                            itemBuilder: (context, index) {
                              final char = _keys[index];
                              return RemoteTarget(
                                key: ValueKey('tv-key-$char'),
                                autofocus: index == 0,
                                radius: 10,
                                padding: EdgeInsets.zero,
                                onPressed: () => _onKeyPress(char),
                                child: Center(
                                  child: Text(
                                    char,
                                    style: const TextStyle(fontSize: 19, fontWeight: FontWeight.bold),
                                  ),
                                ),
                              );
                            },
                          ),
                        ),
                        const SizedBox(height: 8),
                        // 底部三大功能键
                        Row(
                          children: [
                            Expanded(
                              child: RemoteButton(
                                label: '退格',
                                icon: Icons.backspace_outlined,
                                onPressed: _query.isNotEmpty ? _onBackspace : null,
                              ),
                            ),
                            const SizedBox(width: 8),
                            Expanded(
                              child: RemoteButton(
                                label: '清空',
                                icon: Icons.delete_outline_rounded,
                                onPressed: _query.isNotEmpty ? _onClear : null,
                              ),
                            ),
                            const SizedBox(width: 8),
                            Expanded(
                              child: RemoteButton(
                                label: '搜索',
                                icon: Icons.search_rounded,
                                selected: true,
                                onPressed: () => _submit(_query),
                              ),
                            ),
                          ],
                        ),
                      ],
                    ),
                  ),
                  const SizedBox(width: 20),
                  const VerticalDivider(width: 1),
                  const SizedBox(width: 20),
                  // 右侧联想与匹配结果
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          _query.isEmpty ? '历史搜索' : (_searching ? '正在匹配...' : '匹配结果 (${_results.length})'),
                          style: TextStyle(
                            fontSize: 16,
                            fontWeight: FontWeight.bold,
                            color: theme.colorScheme.primary,
                          ),
                        ),
                        const SizedBox(height: 10),
                        Expanded(
                          child: _query.isEmpty
                              ? (widget.recentSearches.isEmpty
                                  ? Center(
                                      child: Text(
                                        '暂无历史搜索\n用左侧键盘按首字母搜剧',
                                        textAlign: TextAlign.center,
                                        style: TextStyle(color: theme.hintColor),
                                      ),
                                    )
                                  : SingleChildScrollView(
                                      child: Wrap(
                                        spacing: 8,
                                        runSpacing: 8,
                                        children: [
                                          for (final item in widget.recentSearches)
                                            RemoteButton(
                                              key: ValueKey('recent-$item'),
                                              label: item,
                                              onPressed: () => _submit(item),
                                            ),
                                        ],
                                      ),
                                    ))
                              : (_results.isEmpty
                                  ? Center(
                                      child: Text(
                                        _searching ? '搜索中...' : '按【搜索】直接查找 “$_query”',
                                        style: TextStyle(color: theme.hintColor),
                                      ),
                                    )
                                  : ListView.separated(
                                      itemCount: _results.length,
                                      separatorBuilder: (_, __) => const SizedBox(height: 6),
                                      itemBuilder: (context, index) {
                                        final item = _results[index];
                                        return RemoteButton(
                                          key: ValueKey('suggest-$index'),
                                          label: item,
                                          icon: Icons.movie_outlined,
                                          onPressed: () => _submit(item),
                                        );
                                      },
                                    )),
                        ),
                      ],
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// 电视端焦点滚动容器：包在 ListView/GridView 外层，
/// 页内任何可聚焦子项获得焦点时自动滚动至可见（D-pad 不会触发指针滚动）。
class TelevisionFocusScroller extends StatefulWidget {
  const TelevisionFocusScroller({
    super.key,
    required this.child,
    this.alignment = 0.25,
  });

  final Widget child;
  final double alignment;

  @override
  State<TelevisionFocusScroller> createState() =>
      _TelevisionFocusScrollerState();
}

class _TelevisionFocusScrollerState extends State<TelevisionFocusScroller> {
  late final FocusManager _manager = FocusManager.instance;
  VoidCallback? _listener;

  @override
  void initState() {
    super.initState();
    _listener = _onFocusChanged;
    _manager.addListener(_listener!);
  }

  void _onFocusChanged() {
    final focusContext = _manager.primaryFocus?.context;
    if (focusContext == null || !mounted) return;
    try {
      final owner = focusContext.findAncestorStateOfType<
        _TelevisionFocusScrollerState
      >();
      if (owner != this) return;
    } catch (_) {
      return;
    }
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted) return;
      final target = _manager.primaryFocus?.context;
      if (target == null) return;
      Scrollable.ensureVisible(
        target,
        alignment: widget.alignment,
        duration: const Duration(milliseconds: 120),
      );
    });
  }

  @override
  void dispose() {
    if (_listener != null) _manager.removeListener(_listener!);
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => widget.child;
}

/// 电视端列表项：RemoteTarget 焦点壳 + 原版 ListTile 视觉，
/// 整行可聚焦、OK 键激活，替代裸 ListTile（后者无 TV 焦点反馈）。
class TvTile extends StatelessWidget {
  const TvTile({
    super.key,
    required this.title,
    this.subtitle,
    this.leading,
    this.trailing,
    this.onTap,
    this.autofocus = false,
    this.enabled = true,
  });

  final Widget title;
  final Widget? subtitle;
  final Widget? leading;
  final Widget? trailing;
  final VoidCallback? onTap;
  final bool autofocus;
  final bool enabled;

  @override
  Widget build(BuildContext context) => RemoteTarget(
    onPressed: enabled ? onTap : null,
    autofocus: autofocus,
    child: ListTile(
      contentPadding: EdgeInsets.zero,
      leading: leading,
      title: title,
      subtitle: subtitle,
      // 激活与焦点均由 RemoteTarget 接管，ListTile 本身不响应点击
      onTap: null,
      trailing: trailing,
      enabled: enabled,
    ),
  );
}

/// 电视端开关列表项：整行聚焦，OK 键切换开关。
class TvSwitchTile extends StatelessWidget {
  const TvSwitchTile({
    super.key,
    required this.value,
    required this.onChanged,
    required this.title,
    this.subtitle,
    this.autofocus = false,
  });

  final bool value;
  final ValueChanged<bool>? onChanged;
  final Widget title;
  final Widget? subtitle;
  final bool autofocus;

  @override
  Widget build(BuildContext context) => RemoteTarget(
    onPressed: onChanged == null ? null : () => onChanged!(!value),
    autofocus: autofocus,
    child: SwitchListTile(
      contentPadding: EdgeInsets.zero,
      value: value,
      // 交互由 RemoteTarget 接管，开关仅作状态展示
      onChanged: null,
      title: title,
      subtitle: subtitle,
    ),
  );
}

/// 电视端复选列表项：整行聚焦，OK 键切换勾选。
class TvCheckboxTile extends StatelessWidget {
  const TvCheckboxTile({
    super.key,
    required this.value,
    required this.onChanged,
    required this.title,
    this.subtitle,
    this.autofocus = false,
  });

  final bool value;
  final ValueChanged<bool>? onChanged;
  final Widget title;
  final Widget? subtitle;
  final bool autofocus;

  @override
  Widget build(BuildContext context) => RemoteTarget(
    onPressed: onChanged == null ? null : () => onChanged!(!value),
    autofocus: autofocus,
    child: CheckboxListTile(
      contentPadding: EdgeInsets.zero,
      value: value,
      // 交互由 RemoteTarget 接管，复选框仅作状态展示
      onChanged: null,
      title: title,
      subtitle: subtitle,
    ),
  );
}

/// 电视端通用文本/数字输入对话框：自建虚拟键盘，彻底杜绝系统软键盘。
/// 返回输入内容（取消返回 null）。
Future<String?> showTelevisionTextInput(
  BuildContext context, {
  required String title,
  String initialValue = '',
  bool numericOnly = false,
  bool obscureText = false,
  int? maxLength,
  String? hint,
}) => showDialog<String>(
  context: context,
  builder: (_) => TelevisionTextInputDialog(
    title: title,
    initialValue: initialValue,
    numericOnly: numericOnly,
    obscureText: obscureText,
    maxLength: maxLength,
    hint: hint,
  ),
);

class TelevisionTextInputDialog extends StatefulWidget {
  const TelevisionTextInputDialog({
    super.key,
    required this.title,
    this.initialValue = '',
    this.numericOnly = false,
    this.obscureText = false,
    this.maxLength,
    this.hint,
  });

  final String title;
  final String initialValue;
  final bool numericOnly;
  final bool obscureText;
  final int? maxLength;
  final String? hint;

  @override
  State<TelevisionTextInputDialog> createState() =>
      _TelevisionTextInputDialogState();
}

class _TelevisionTextInputDialogState extends State<TelevisionTextInputDialog> {
  late String _value = widget.initialValue;

  static const _textKeys = [
    'A', 'B', 'C', 'D', 'E', 'F',
    'G', 'H', 'I', 'J', 'K', 'L',
    'M', 'N', 'O', 'P', 'Q', 'R',
    'S', 'T', 'U', 'V', 'W', 'X',
    'Y', 'Z', '1', '2', '3', '4',
    '5', '6', '7', '8', '9', '0',
  ];
  static const _numberKeys = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '0'];

  bool get _canAppend => widget.maxLength == null ||
      _value.length < widget.maxLength!;

  void _append(String char) {
    if (!_canAppend) return;
    setState(() => _value += char);
  }

  void _backspace() {
    if (_value.isNotEmpty) {
      setState(() => _value = _value.substring(0, _value.length - 1));
    }
  }

  void _clear() {
    if (_value.isNotEmpty) setState(() => _value = '');
  }

  void _submit() => Navigator.pop(context, _value.trim());

  String get _display => widget.obscureText ? '•' * _value.length : _value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final keys = widget.numericOnly ? _numberKeys : _textKeys;
    return AlertDialog(
      titlePadding: const EdgeInsets.fromLTRB(24, 20, 24, 12),
      contentPadding: const EdgeInsets.fromLTRB(24, 0, 24, 20),
      title: Row(
        children: [
          Icon(Icons.keyboard_alt_outlined, color: theme.colorScheme.primary),
          const SizedBox(width: 10),
          Expanded(child: Text(widget.title)),
        ],
      ),
      content: SizedBox(
        width: 560,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            // 只读显示条，焦点与激活全部由虚拟键盘承担
            Container(
              height: 56,
              padding: const EdgeInsets.symmetric(horizontal: 16),
              alignment: Alignment.centerLeft,
              decoration: BoxDecoration(
                color: theme.colorScheme.surfaceContainerHighest.withValues(
                  alpha: .5,
                ),
                borderRadius: BorderRadius.circular(14),
                border: Border.all(
                  color: theme.colorScheme.primary.withValues(alpha: .3),
                ),
              ),
              child: Text(
                _value.isEmpty
                    ? (widget.hint ?? (widget.numericOnly ? '输入数字' : '输入内容'))
                    : _display,
                style: TextStyle(
                  fontSize: 22,
                  fontWeight: _value.isEmpty ? FontWeight.normal : FontWeight.bold,
                  color: _value.isEmpty
                      ? theme.hintColor
                      : theme.colorScheme.onSurface,
                  letterSpacing: _value.isEmpty ? 0 : 2,
                ),
              ),
            ),
            const SizedBox(height: 16),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              alignment: WrapAlignment.center,
              children: [
                for (final (index, key) in keys.indexed)
                  RemoteButton(
                    key: ValueKey('tv-key-$key-$index'),
                    label: key,
                    autofocus: index == 0,
                    onPressed: () => _append(key),
                  ),
              ],
            ),
            const SizedBox(height: 12),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              alignment: WrapAlignment.center,
              children: [
                RemoteButton(
                  label: '退格',
                  icon: Icons.backspace_outlined,
                  onPressed: _backspace,
                ),
                RemoteButton(
                  label: '清空',
                  icon: Icons.clear_all_rounded,
                  onPressed: _value.isEmpty ? null : _clear,
                ),
                RemoteButton(
                  label: '确定',
                  icon: Icons.check_rounded,
                  onPressed: _submit,
                ),
              ],
            ),
          ],
        ),
      ),
      actions: [
        RemoteButton(
          label: '取消',
          onPressed: () => Navigator.pop(context),
        ),
      ],
    );
  }
}

/// 电视端只读搜索条：点击弹出虚拟键盘输入，杜绝系统软键盘。
/// 再次打开后清空并确定即可清空搜索。
class TelevisionSearchBar extends StatelessWidget {
  const TelevisionSearchBar({
    super.key,
    required this.value,
    required this.hint,
    required this.onSubmit,
    this.title,
  });

  final String value;
  final String hint;
  final ValueChanged<String> onSubmit;
  final String? title;

  Future<void> _openInput(BuildContext context) async {
    final text = await showTelevisionTextInput(
      context,
      title: title ?? hint,
      initialValue: value,
      maxLength: 40,
    );
    if (text != null) onSubmit(text.trim());
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return RemoteTarget(
      onPressed: () => _openInput(context),
      child: Container(
        height: 56,
        padding: const EdgeInsets.symmetric(horizontal: 16),
        decoration: BoxDecoration(
          color: theme.colorScheme.surfaceContainerHighest.withValues(alpha: .4),
          borderRadius: BorderRadius.circular(14),
          border: Border.all(
            color: theme.colorScheme.primary.withValues(alpha: .3),
          ),
        ),
        child: Row(
          children: [
            Icon(Icons.search_rounded, color: theme.colorScheme.primary),
            const SizedBox(width: 12),
            Expanded(
              child: Text(
                value.isEmpty ? hint : value,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: TextStyle(
                  fontSize: 19,
                  fontWeight: value.isEmpty
                      ? FontWeight.normal
                      : FontWeight.bold,
                  color: value.isEmpty
                      ? theme.hintColor
                      : theme.colorScheme.onSurface,
                ),
              ),
            ),
            if (value.isNotEmpty)
              Icon(
                Icons.edit_note_rounded,
                size: 22,
                color: theme.hintColor,
              ),
          ],
        ),
      ),
    );
  }
}
