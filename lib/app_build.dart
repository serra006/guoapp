const allSourcesEnabled = bool.fromEnvironment('ALL_SOURCES');
const tvBuildEnabled = bool.fromEnvironment('TV_BUILD');
const appName = tvBuildEnabled ? '真果TV' : '真果鉴';
const appSlug =
    '${allSourcesEnabled ? 'zhenguojian' : 'hongguojian'}${tvBuildEnabled ? '-tv' : ''}';
