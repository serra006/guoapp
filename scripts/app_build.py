import base64
from dataclasses import dataclass


@dataclass(frozen=True)
class BuildVariant:
    all_sources: bool = False
    flavor: str = 'phone'  # phone=手机/平板, tv=电视/盒子(独立包名)

    @property
    def name(self):
        label = '真果鉴' if self.all_sources else '红果鉴'
        return f'{label} TV' if self.flavor == 'tv' else label

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
                        help='构建包含全部站源的真果鉴；默认构建仅红果的红果鉴')
    parser.add_argument('--flavor', choices=['phone', 'tv'], default='phone',
                        help='tv=电视版（独立包名 com.duanju.duanju_app.tv，仅电视桌面可见）')
