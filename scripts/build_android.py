import argparse
import os
import platform
import shutil
import subprocess
import sys
from pathlib import Path

from build_mirrors import china_mirror_environment, mirrored_pub_lockfile
from app_build import BuildVariant, add_variant_argument

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--abi', action='append', choices=['arm64-v8a', 'armeabi-v7a', 'x86_64'])
parser.add_argument('--cn-mirrors', action='store_true', help='使用 Flutter 中国镜像和阿里云 Maven 镜像')
parser.add_argument('--universal', action='store_true',
                    help='手机端：不按 ABI 拆分，产出一个含 32/64 位的通用包')
add_variant_argument(parser)
options = parser.parse_args()
variant = BuildVariant(options.all_sources, options.flavor)
environment = os.environ.copy()
if platform.system() == 'Darwin':
    environment['LANG'] = 'en_US.UTF-8'
    environment['LC_ALL'] = 'en_US.UTF-8'
environment.setdefault('GOPROXY', 'https://goproxy.cn,direct')
environment.setdefault('GOSUMDB', 'off')
flutter = shutil.which('flutter')
if not flutter:
    raise SystemExit('请先将 Flutter SDK 的 bin 目录加入 PATH。')
if options.universal and options.flavor == 'tv':
    # 电视端必须是 ABI 拆分包：老盒子只认 32 位，直装单包会白占空间、还可能装不上。
    # 更关键的是 --universal 会带 --build-number（见下方注释），电视端传了必炸。
    raise SystemExit('--universal 只用于手机端；电视端请按 ABI 分别构建。')
# 通用包不传 --abi 时的默认架构：只留 32/64 位 ARM，绝不带 x86_64。
# 这个默认值必须同时作用到 build_native.py（abi_args）和 --target-platform：
# 只改 target-platform 会留下 x86_64 的原生 so 目录，包照样变大；
# 只改 build_native.py 则 flutter 仍按默认三架构打包。
selected_abis = list(options.abi or [])
if options.universal and not selected_abis:
    selected_abis = ['arm64-v8a', 'armeabi-v7a']
abi_args = [item for abi in selected_abis for item in ['--abi', abi]]
universal_args = ['--universal'] if options.universal else []
with china_mirror_environment(environment, options.cn_mirrors) as env, mirrored_pub_lockfile(root, env):
    if options.cn_mirrors:
        print('本次构建启用国内依赖镜像，保留官方 Maven 仓库备用。', flush=True)
    subprocess.run([sys.executable, str(root / 'scripts' / 'build_native.py'), '--platform', 'android', *abi_args, *variant.arguments],
                   cwd=root, env=env, check=True)
    subprocess.run([flutter, 'pub', 'get'], cwd=root, env=env, check=True)
    # --universal 时不传 --split-per-abi（用条件拼接，不留空字符串元素），
    # Flutter 会产出一个包含所有 --target-platform ABI 的通用 APK。
    split_args = [] if options.universal else ['--split-per-abi']
    build_args = [flutter, 'build', 'apk', '--release', *split_args, '--no-pub',
                  *variant.flutter_arguments, *variant.build_name_arguments]
    build_number = variant.build_number
    if options.universal:
        # 通用包 versionCode = pubspec 构建号 + 5000（0.2.78+85 → 5085）。
        # 必要性：--split-per-abi 时 Flutter 会给每个 split 加 versionCode 偏移
        #（armeabi-v7a +1000、arm64-v8a +2000、x86_64 +4000），历史上手机端发布的
        # 是 1084/2084/4084。取消拆分后 versionCode 会退回 base（84），已安装
        # 0.2.77（2084）的设备会因 versionCode 回落被系统拒绝覆盖安装。
        # +5000 保证通用包大于历史所有 split 包（最大 4084），升级链不断。
        # 电视端绝对不要传 --build-number：两端共用 pubspec 的 `+N` 计数器，
        # 把某一端钉到另一个基数上会让另一端的 versionCode 回落，覆盖安装直接失败。
        build_number = str(int(variant.build_number) + 5000)
        build_args += ['--build-number', build_number]
    print(f'版本名 {variant.version}（{variant.flavor}），构建号 {build_number}', flush=True)
    if selected_abis:
        targets = {'arm64-v8a': 'android-arm64', 'armeabi-v7a': 'android-arm', 'x86_64': 'android-x64'}
        build_args += ['--target-platform', ','.join(targets[abi] for abi in selected_abis)]
    subprocess.run(build_args, cwd=root, env=env, check=True)
    subprocess.run([sys.executable, str(root / 'scripts' / 'package_release.py'), '--platform', 'android',
                    *abi_args, *variant.arguments, *universal_args],
                   cwd=root, env=env, check=True)
