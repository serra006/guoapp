import 'package:flutter/material.dart';

import 'app_layout.dart';
import 'local_store.dart';
import 'models.dart';
import 'remote_widgets.dart';

/// 站源密码锁弹窗：启用 / 关闭密码功能，以及解锁 / 重新锁定隐藏站源。
Future<void> showSourceGateDialog(BuildContext context, LocalStore store) =>
    showDialog<void>(
      context: context,
      builder: (_) => SourceGateDialog(store: store),
    );

class SourceGateDialog extends StatefulWidget {
  const SourceGateDialog({super.key, required this.store});
  final LocalStore store;
  @override
  State<SourceGateDialog> createState() => _SourceGateDialogState();
}

class _SourceGateDialogState extends State<SourceGateDialog> {
  final _pin = TextEditingController();
  final _confirm = TextEditingController();
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _pin.dispose();
    _confirm.dispose();
    super.dispose();
  }

  Future<void> _run(Future<void> Function() action) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await action();
      if (mounted) Navigator.pop(context);
    } catch (error) {
      if (mounted) {
        setState(() {
          _busy = false;
          _error = error.toString();
        });
      }
    }
  }

  Future<void> _unlock() async {
    final pin = _pin.text;
    await _run(() => widget.store.unlockSources(pin));
  }

  Future<void> _enable() async {
    if (_pin.text != _confirm.text) {
      setState(() => _error = '两次输入的密码不一致');
      return;
    }
    final pin = _pin.text;
    await _run(() => widget.store.enableSourceGate(pin));
  }

  Future<void> _lock() async {
    setState(() {
      _busy = false;
      _error = null;
    });
    widget.store.lockSources();
    if (mounted) Navigator.pop(context);
  }

  Future<void> _disable() async {
    await _run(widget.store.disableSourceGate);
  }

  /// 密码输入框：触屏端用系统输入框，电视端用自建虚拟键盘。
  ///
  /// 项目在电视端刻意清除了系统软键盘（见 TV-CHANGES 第二节「系统软键盘清除」5 处），
  /// 所以电视上原生 TextField 没有任何键盘可弹，密码永远输不进去。
  /// 这里按 AppLayout.isTelevision 分流，触屏端行为与改动前逐字节一致。
  Widget _pinInput(
    String label,
    TextEditingController controller, {
    bool autofocus = false,
  }) {
    if (!AppLayout.isTelevision(context)) {
      return TextField(
        controller: controller,
        autofocus: autofocus,
        obscureText: true,
        keyboardType: TextInputType.number,
        decoration: InputDecoration(labelText: label),
      );
    }
    return OutlinedButton.icon(
      autofocus: autofocus,
      icon: const Icon(Icons.dialpad_rounded),
      label: Text(
        controller.text.isEmpty
            ? '$label：点此用遥控器输入'
            : '$label：已输入 ${controller.text.length} 位数字',
      ),
      onPressed: () async {
        final value = await showTelevisionTextInput(
          context,
          title: label,
          numericOnly: true,
          obscureText: true,
          maxLength: 12,
          hint: '3–12 位数字',
        );
        // 自建键盘是异步弹窗，回来时本弹窗可能已经被 pop 掉。
        if (value != null && mounted) {
          setState(() => controller.text = value);
        }
      },
    );
  }

  @override
  Widget build(BuildContext context) {
    final store = widget.store;
    return AnimatedBuilder(
      animation: store,
      builder: (context, _) {
        final enabled = store.sourceGateEnabled;
        final gateOff = store.sourceGateOff;
        final admin = store.profile.admin;
        return AlertDialog(
          title: const Text('站源密码锁'),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (!enabled && gateOff) ...[
                Text(
                  '当前未使用密码，已显示全部站源。'
                  '${admin ? '可重新设置密码以隐藏敏感站源。' : ''}',
                ),
                if (admin) ...[
                  const SizedBox(height: 16),
                  _pinInput('设置密码（3 至 12 位数字）', _pin, autofocus: true),
                  const SizedBox(height: 12),
                  _pinInput('再次输入密码', _confirm),
                ] else
                  const Padding(
                    padding: EdgeInsets.only(top: 8),
                    child: Text('只有管理员用户可以设置密码。'),
                  ),
              ] else if (!enabled) ...[
                Text(
                  '启用后，除'
                  '${SourceSite.primaryValues.map((site) => site.name).join('、')}'
                  '外的站源将默认隐藏，输入密码后才显示。',
                ),
                const SizedBox(height: 16),
                if (!admin)
                  const Text('只有管理员用户可以启用或关闭密码功能。')
                else ...[
                  _pinInput('设置密码（3 至 12 位数字）', _pin, autofocus: true),
                  const SizedBox(height: 12),
                  _pinInput('再次输入密码', _confirm),
                ],
              ] else if (!store.sourcesUnlocked) ...[
                const Text('隐藏的站源已锁定，输入密码后显示。'),
                const SizedBox(height: 16),
                // 自建键盘没有 onSubmitted，电视端点下方「解锁」按钮即可。
                _pinInput('密码', _pin, autofocus: true),
              ] else ...[
                const Text('隐藏的站源已解锁，重启应用后会恢复隐藏。'),
              ],
              if (_error != null)
                Padding(
                  padding: const EdgeInsets.only(top: 12),
                  child: Text(
                    _error!,
                    style: TextStyle(
                      color: Theme.of(context).colorScheme.error,
                    ),
                  ),
                ),
            ],
          ),
          actions: [
            TextButton(
              onPressed: _busy ? null : () => Navigator.pop(context),
              child: const Text('取消'),
            ),
            if (enabled && store.sourcesUnlocked && admin)
              TextButton(
                onPressed: _busy ? null : () => _run(store.disableSourceGate),
                child: const Text('关闭密码功能'),
              ),
            if (enabled && store.sourcesUnlocked)
              FilledButton(
                onPressed: _busy ? null : _lock,
                child: const Text('重新锁定'),
              ),
            if (enabled && !store.sourcesUnlocked)
              FilledButton(
                onPressed: _busy ? null : _unlock,
                child: Text(_busy ? '正在验证…' : '解锁'),
              ),
            // 未启用密码时，管理员可直接选择「不使用密码」关闭隐藏、显示全部站源。
            if (!enabled && !gateOff && admin)
              TextButton(
                onPressed: _busy ? null : _disable,
                child: const Text('不使用密码'),
              ),
            if (!enabled && admin)
              FilledButton(
                onPressed: _busy ? null : _enable,
                child: Text(_busy ? '正在保存…' : '启用密码锁'),
              ),
          ],
        );
      },
    );
  }
}
