import argparse
import hashlib
import shutil
import zipfile
from pathlib import Path

from app_build import BuildVariant, add_variant_argument

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--platform', choices=['android', 'windows'], required=True)
parser.add_argument('--abi', action='append', choices=['arm64-v8a', 'armeabi-v7a', 'x86_64'])
parser.add_argument('--universal', action='store_true',
                    help='手机端：产物是不按 ABI 拆分的通用包（打包时需同时带 --universal）')
add_variant_argument(parser)
options = parser.parse_args()
variant = BuildVariant(options.all_sources, options.flavor)
if options.universal and options.flavor == 'tv':
    # 与 build_android.py 同一条守卫：通用包会带 --build-number，电视端绝不能用。
    # 这里也拦一遍，因为本脚本是唯一能往 dist/ 写产物的入口，手搓 fat TV 包同样要挡住。
    raise SystemExit('--universal 只用于手机端；电视端请按 ABI 分别构建。')
# 版本名按 flavor 取：电视端走 versions.json 的独立版本线，手机端沿用 pubspec。
version = variant.version
output = root / 'dist' / options.platform
output.mkdir(parents=True, exist_ok=True)
artifacts = []

if options.platform == 'android':
    # --flavor 始终指定，产物文件名一律携带 flavor 中缀（如 app-arm64-v8a-tv-release.apk）
    flavor_infix = '-' + variant.flavor
    # 复制本轮产物之前，先清掉 dist 里同版本号的旧包（如上一轮残留的 -x86_64.apk）。
    # 两个分支共用：否则切到 split 打包时，先前留下的 -universal.apk 会既不被删、
    # 又被下面的 SHA256SUMS 通配算进去，最后被 workflow 的 dist/android/* 一起上传。
    # 必须放在所有 shutil.copy2 之前，否则会把本轮刚产出的包删掉。
    for stale in output.glob(f'{variant.slug}-{version}-*.apk'):
        stale.unlink()
    if options.universal:
        # 通用包：构建时没有 --split-per-abi，文件名里没有 ABI 中缀（app-phone-release.apk）。
        source = root / 'build' / 'app' / 'outputs' / 'flutter-apk' / f'app{flavor_infix}-release.apk'
        if not source.is_file():
            raise SystemExit('缺少 APK：' + str(source))
        universal_libraries = ['libduanju_core.so', 'libflutter.so', 'libapp.so', 'libmpv.so', 'libffmpegkit.so']
        universal_abis = ['arm64-v8a', 'armeabi-v7a']
        with zipfile.ZipFile(source) as archive:
            universal_names = set(archive.namelist())
        # 通用包必须两套 ABI 的 so 都在：本机无法编译，这里若放过缺库的包，
        # 用户装上就是一路闪退，且 CI 也只会看到「构建成功」。缺哪个 ABI 的哪个库都点名报出来。
        universal_missing = [f'lib/{abi}/{library}' for abi in universal_abis for library in universal_libraries
                             if f'lib/{abi}/{library}' not in universal_names]
        if universal_missing:
            raise SystemExit('通用 APK 缺少原生库：' + ', '.join(universal_missing))
        target = output / f'{variant.slug}-{version}-universal.apk'
        shutil.copy2(source, target)
        artifacts.append(target)
    else:
        for abi in options.abi or ['arm64-v8a', 'armeabi-v7a', 'x86_64']:
            source = root / 'build' / 'app' / 'outputs' / 'flutter-apk' / f'app-{abi}{flavor_infix}-release.apk'
            if not source.is_file():
                raise SystemExit('缺少 APK：' + str(source))
            with zipfile.ZipFile(source) as archive:
                names = set(archive.namelist())
                required = [f'lib/{abi}/{library}' for library in
                            ['libduanju_core.so', 'libflutter.so', 'libapp.so', 'libmpv.so', 'libffmpegkit.so']]
                missing = set(required) - names
                if missing:
                    raise SystemExit('APK 缺少原生库：' + ', '.join(sorted(missing)))
            target = output / f'{variant.slug}-{version}-{abi}.apk'
            shutil.copy2(source, target)
            artifacts.append(target)
else:
    bundle = root / 'build' / 'windows' / 'x64' / 'runner' / 'Release'
    required = ['zhenguojian.exe', 'duanju_core.dll', 'flutter_windows.dll', 'libffmpegkit.dll',
                'libmpv-2.dll', 'msvcp140.dll', 'vcruntime140.dll',
                'data/icudtl.dat', 'data/app.so']
    missing = [name for name in required if not (bundle / name).is_file()]
    if missing:
        raise SystemExit('Windows 安装包缺少文件：' + ', '.join(missing))
    target = output / f'{variant.slug}-{version}-windows-x64.zip'
    with zipfile.ZipFile(target, 'w', zipfile.ZIP_DEFLATED) as archive:
        for source in sorted(bundle.rglob('*')):
            if source.is_file():
                relative = source.relative_to(bundle).as_posix()
                if relative == 'zhenguojian.exe':
                    relative = variant.slug + '.exe'
                archive.write(source, relative)
    artifacts.append(target)

checksums = []
for artifact in sorted(output.glob(f'*-{version}-*')):
    digest = hashlib.sha256()
    with artifact.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    checksums.append(f'{digest.hexdigest()}  {artifact.name}')
    print(artifact)
(output / 'SHA256SUMS.txt').write_text('\n'.join(checksums) + '\n', encoding='ascii')
