import argparse
import hashlib
import os
import re
import subprocess
import zipfile
from pathlib import Path

from app_build import BuildVariant, add_variant_argument


root = Path(__file__).resolve().parents[1]
certificate = '872c569de39fce8ceb34a81bdd10e798d2019c79420750ae1bc79d5c5e74c3c9'


def android_tool(name):
    home = Path(os.environ.get('ANDROID_HOME') or os.environ.get('ANDROID_SDK_ROOT', ''))
    matches = sorted(home.glob('build-tools/*/' + name))
    if not matches:
        raise SystemExit('找不到 Android 构建工具：' + name)
    return str(matches[-1])


def main():
    parser = argparse.ArgumentParser()
    add_variant_argument(parser)
    variant = BuildVariant(parser.parse_args().all_sources)
    version = re.search(r'^version:\s*(\S+)', (root / 'pubspec.yaml').read_text(encoding='utf-8'), re.MULTILINE).group(1)
    version_name, build = version.split('+')
    apk = root / 'dist' / 'android' / f'{variant.slug}-{version}-arm64-v8a.apk'
    if not apk.is_file():
        raise SystemExit('缺少 APK：' + str(apk))
    checksums = (apk.parent / 'SHA256SUMS.txt').read_text(encoding='ascii').splitlines()
    digest = hashlib.sha256(apk.read_bytes()).hexdigest()
    if f'{digest}  {apk.name}' not in checksums:
        raise SystemExit('APK SHA-256 不匹配')
    signed = subprocess.run([android_tool('apksigner'), 'verify', '--verbose', '--print-certs', str(apk)],
                            check=True, capture_output=True, text=True).stdout
    if 'Verified using v2 scheme (APK Signature Scheme v2): true' not in signed:
        raise SystemExit('APK 缺少 v2 签名')
    signers = re.findall(r'certificate SHA-256 digest:\s*([0-9a-fA-F]+)', signed)
    if not signers or any(signer.lower() != certificate for signer in signers):
        raise SystemExit('APK 签名证书不匹配')
    badging = subprocess.run([android_tool('aapt'), 'dump', 'badging', str(apk)],
                             check=True, capture_output=True, text=True).stdout
    package = re.search(r"^package: name='([^']+)' versionCode='(\d+)' versionName='([^']+)'", badging, re.MULTILINE)
    if not package or package.groups() != ('com.duanju.duanju_app', str(2000 + int(build)), version_name):
        raise SystemExit('APK 应用 ID 或版本不匹配')
    if f"application-label:'{variant.name}'" not in badging:
        raise SystemExit('APK 应用名称不匹配')
    with zipfile.ZipFile(apk) as archive:
        names = set(archive.namelist())
    required = {f'lib/arm64-v8a/{name}' for name in
                ('libduanju_core.so', 'libflutter.so', 'libapp.so', 'libmpv.so', 'libffmpegkit.so')}
    if required - names or any(name.startswith(('lib/armeabi-v7a/', 'lib/x86_64/')) for name in names):
        raise SystemExit('APK 原生库或 ABI 不匹配')
    print(f'{apk.name}: 签名、应用 ID、版本、名称、arm64 原生库和 SHA-256 均匹配')


if __name__ == '__main__':
    main()
