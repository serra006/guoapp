const allSourcesEnabled = bool.fromEnvironment('ALL_SOURCES');
const tvBuildEnabled = bool.fromEnvironment('TV_BUILD');
const appName = tvBuildEnabled
    ? (allSourcesEnabled ? '真果TV 多源版' : '真果TV')
    : (allSourcesEnabled ? '真果鉴' : '红果鉴');
const appSlug =
    '${allSourcesEnabled ? 'zhenguojian' : 'hongguojian'}${tvBuildEnabled ? '-tv' : ''}';
