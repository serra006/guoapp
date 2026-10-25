import base64
import json
import re
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def pubspec_version() -> str:
    """pubspec.yaml 里的版本名（不含 +构建号）。手机端沿用这条版本线。"""
    text = (ROOT / 'pubspec.yaml').read_text(encoding='utf-8')
    match = re.search(r'^version:\s*([\w.+-]+)\s*$', text, re.MULTILINE)
    if not match:
        raise SystemExit('pubspec.yaml 缺少合法版本号。')
    return match.group(1).split('+')[0]


def pubspec_build_number() -> str:
    text = (ROOT / 'pubspec.yaml').read_text(encoding='utf-8')
    match = re.search(r'^version:\s*[\w.+-]*\+(\d+)\s*$', text, re.MULTILINE)
    return match.group(1) if match else '0'


def tv_version() -> str:
    """电视端独立版本线，写在 versions.json 的 "tv" 字段。"""
    data = json.loads((ROOT / 'versions.json').read_text(encoding='utf-8'))
    version = str(data.get('tv') or '').strip()
    if not version:
        raise SystemExit('versions.json 缺少 "tv" 版本号。')
    return version


@dataclass(frozen=True)
class BuildVariant:
    all_sources: bool = False
    flavor: str = 'phone'  # phone=手机/平板, tv=电视/盒子(独立包名)

    def __post_init__(self):
        # 电视版只有一个发行版「真果TV」，固定走全站源，
        # 防止漏传 --all-sources 产出「名字对、站源少」的错误包
        if self.flavor == 'tv' and not self.all_sources:
            object.__setattr__(self, 'all_sources', True)

    @property
    def name(self):
        if self.flavor == 'tv':
            return '真果TV'
        return '真果鉴' if self.all_sources else '红果鉴'

    @property
    def slug(self):
        slug = 'zhenguojian' if self.all_sources else 'hongguojian'
        return f'{slug}-tv' if self.flavor == 'tv' else slug

    @property
    def version(self):
        """版本名。电视端独立走 versions.json（当前 1.0.x），手机端沿用 pubspec。"""
        return tv_version() if self.flavor == 'tv' else pubspec_version()

    @property
    def build_number(self):
        """构建号（= versionCode 基数）。两端共用一个计数器，保证各自单调递增。"""
        return pubspec_build_number()

    @property
    def arguments(self):
        arguments = ['--all-sources'] if self.all_sources else []
        if self.flavor == 'tv':
            arguments += ['--flavor', 'tv']
        return arguments

    @property
    def flutter_arguments(self):
        # 引入 flavor 后 Gradle task 为 assemble{Phone,Tv}{Release,...}，
        # 必须始终显式指定 --flavor，否则裸构建会因 task 缺失而失败
        return [
            '--flavor', self.flavor,
            '--dart-define=ALL_SOURCES=' + str(self.all_sources).lower(),
            '--dart-define=APP_VERSION=' + self.version,
        ] + (['--dart-define=TV_BUILD=true'] if self.flavor == 'tv' else [])

    @property
    def build_name_arguments(self):
        """只有电视端覆盖版本名；手机端留给 pubspec。

        刻意不传 --build-number：构建号统一由 pubspec 的 `+N` 决定，
        否则电视端和手机端的 versionCode 会各自回到 1，覆盖安装直接失败。
        """
        return ['--build-name', self.version] if self.flavor == 'tv' else []

    @property
    def linker_flags(self):
        return '-s -w -X duanjuapp/native/core.buildAllSources=' + str(self.all_sources).lower()

    @classmethod
    def from_dart_defines(cls, encoded):
        values = {}
        for item in encoded.split(','):
            if not item:
                continue
            key, separator, value = base64.b64decode(item, validate=True).decode('utf-8').partition('=')
            if separator:
                values[key] = value
        return cls(values.get('ALL_SOURCES') == 'true')


def add_variant_argument(parser):
    parser.add_argument('--all-sources', action='store_true',
                        help='构建包含全部站源的多源版（电视版固定为全站源，无需此参数）')
    parser.add_argument('--flavor', choices=['phone', 'tv'], default='phone',
                        help='tv=电视版「真果TV」（包名 com.duanju.zhenguo.tv，仅电视桌面可见）')
