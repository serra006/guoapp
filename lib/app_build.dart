const allSourcesEnabled = bool.fromEnvironment('ALL_SOURCES');
const tvBuildEnabled = bool.fromEnvironment('TV_BUILD');
const appName =
    '${allSourcesEnabled ? '真果鉴' : '红果鉴'}${tvBuildEnabled ? ' TV' : ''}';
const appSlug =
    '${allSourcesEnabled ? 'zhenguojian' : 'hongguojian'}${tvBuildEnabled ? '-tv' : ''}';
