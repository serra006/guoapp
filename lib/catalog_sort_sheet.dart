import 'package:flutter/material.dart';

import 'app_layout.dart';
import 'catalog_sort.dart';
import 'remote_widgets.dart';

Future<CatalogView?> chooseCatalogView(
  BuildContext context,
  CatalogView current,
) => AppLayout.isTelevision(context)
    ? _chooseCatalogViewTelevision(context, current)
    : _chooseCatalogViewTouch(context, current);

/// 电视端：居中对话框 + 遥控按钮网格，选项即时高亮，「应用」统一生效。
Future<CatalogView?> _chooseCatalogViewTelevision(
  BuildContext context,
  CatalogView current,
) => showDialog<CatalogView>(
  context: context,
  builder: (dialogContext) {
    var selected = current;
    return StatefulBuilder(
      builder: (context, update) => AlertDialog(
        title: const Text('排序与筛选'),
        content: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Text('排序方式'),
              const SizedBox(height: 8),
              Wrap(
                spacing: 10,
                runSpacing: 10,
                children: [
                  for (final (index, sort) in CatalogSort.values.indexed)
                    RemoteButton(
                      label: sort.label,
                      autofocus: index == 0,
                      selected: selected.sort == sort,
                      onPressed: () =>
                          update(() => selected = selected.copyWith(sort: sort)),
                    ),
                ],
              ),
              const SizedBox(height: 20),
              const Text('剧集状态'),
              const SizedBox(height: 8),
              Wrap(
                spacing: 10,
                runSpacing: 10,
                children: [
                  for (final entry in const {
                    '': '全部',
                    'ongoing': '连载中',
                    'finished': '已完结',
                    'unknown': '状态未知',
                  }.entries)
                    RemoteButton(
                      label: entry.value,
                      selected: selected.release == entry.key,
                      onPressed: () => update(
                        () => selected = selected.copyWith(release: entry.key),
                      ),
                    ),
                ],
              ),
              const SizedBox(height: 16),
              Text(
                '排序和筛选作用于已加载的剧集；缺少排序资料的条目排在最后。',
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ],
          ),
        ),
        actions: [
          RemoteButton(
            label: '取消',
            onPressed: () => Navigator.pop(dialogContext),
          ),
          RemoteButton(
            label: '应用',
            onPressed: () => Navigator.pop(dialogContext, selected),
          ),
        ],
      ),
    );
  },
);

/// 触屏端：底部弹层 + ChoiceChip。
Future<CatalogView?> _chooseCatalogViewTouch(
  BuildContext context,
  CatalogView current,
) => showModalBottomSheet<CatalogView>(
  context: context,
  showDragHandle: true,
  isScrollControlled: true,
  useSafeArea: true,
  builder: (context) {
    var selected = current;
    return StatefulBuilder(
      builder: (context, update) => SingleChildScrollView(
        padding: EdgeInsets.fromLTRB(
          20,
          0,
          20,
          20 + MediaQuery.viewInsetsOf(context).bottom,
        ),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('排序与筛选', style: Theme.of(context).textTheme.titleLarge),
            const SizedBox(height: 16),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                for (final sort in CatalogSort.values)
                  ChoiceChip(
                    label: Text(sort.label),
                    selected: selected.sort == sort,
                    onSelected: (_) =>
                        update(() => selected = selected.copyWith(sort: sort)),
                  ),
              ],
            ),
            const SizedBox(height: 24),
            const Text('剧集状态'),
            const SizedBox(height: 8),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                for (final entry in const {
                  '': '全部',
                  'ongoing': '连载中',
                  'finished': '已完结',
                  'unknown': '状态未知',
                }.entries)
                  ChoiceChip(
                    label: Text(entry.value),
                    selected: selected.release == entry.key,
                    onSelected: (_) => update(
                      () => selected = selected.copyWith(release: entry.key),
                    ),
                  ),
              ],
            ),
            const SizedBox(height: 16),
            Text(
              '排序和筛选作用于已加载的剧集；缺少排序资料的条目排在最后。',
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 20),
            SizedBox(
              width: double.infinity,
              child: FilledButton(
                onPressed: () => Navigator.pop(context, selected),
                child: const Text('应用'),
              ),
            ),
          ],
        ),
      ),
    );
  },
);
