import base64
from dataclasses import dataclass


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
        ] + (['--dart-define=TV_BUILD=true'] if self.flavor == 'tv' else [])

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
                        help='tv=电视版「真果TV」（包名 com.duanju.zhenguojian.tv，仅电视桌面可见）')
