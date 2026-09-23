import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path

from app_build import BuildVariant, add_variant_argument

root = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser()
    add_variant_argument(parser)
    variant = BuildVariant(parser.parse_args().all_sources)
    if platform.system() != 'Windows':
        raise SystemExit('此检查需要 Windows。')
    version = re.search(r'^version:\s*(\S+)', (root / 'pubspec.yaml').read_text(encoding='utf-8'), re.MULTILINE).group(1)
    package = root / 'dist' / 'windows' / f'{variant.slug}-{version}-windows-x64.zip'
    portable = root / 'dist' / 'windows' / f'{variant.slug}-{version}-windows-x64-portable.exe'
    sums = (root / 'dist' / 'windows' / 'SHA256SUMS.txt').read_text(encoding='ascii')
    for artifact in (package, portable):
        expected = f'{hashlib.sha256(artifact.read_bytes()).hexdigest()}  {artifact.name}'
        if expected not in sums.splitlines():
            raise SystemExit('Windows 产物 SHA256 校验失败：' + artifact.name)
    with tempfile.TemporaryDirectory(prefix='zhenguojian-smoke-') as temporary:
        directory = Path(temporary)
        with zipfile.ZipFile(package) as archive:
            archive.extractall(directory)
        media = directory / 'fixture.mp4'
        subprocess.run(['ffmpeg', '-v', 'error', '-y', '-f', 'lavfi',
                        '-i', 'testsrc2=size=160x90:rate=12', '-t', '3',
                        '-c:v', 'libx264', '-threads', '1', str(media)], check=True)
        portable_directory = directory / 'portable'
        portable_directory.mkdir()
        executable = portable_directory / portable.name
        shutil.copy2(portable, executable)
        portable_report = directory / 'portable-result.json'
        legacy = [Path(os.environ[name]) / '真果鉴' / '真果鉴'
                  for name in ('APPDATA', 'LOCALAPPDATA')]
        if any(location.exists() for location in legacy):
            raise SystemExit('Windows 测试环境已有旧应用数据，无法验证便携目录写入。')
        subprocess.run([str(executable), '--package-smoke', str(portable_report), str(media)],
                       cwd=portable_directory, check=True, timeout=120)
        portable_evidence = json.loads(portable_report.read_text(encoding='utf-8'))
        if portable_evidence.get('ok') is not True:
            raise SystemExit('Windows 单文件便携包启动验收未通过。')
        home = portable_directory / '.zhenguojian'
        if not (home / 'data' / 'downloads').is_dir() or not (home / 'tmp').is_dir() or not (home / 'data' / 'shared_preferences.json').is_file():
            raise SystemExit('便携包未在 EXE 旁创建数据和临时目录。')
        if any(location.exists() for location in legacy):
            raise SystemExit('便携包在旧 AppData 位置创建了文件。')
        if set(portable_directory.iterdir()) != {executable, home}:
            raise SystemExit('便携包在 EXE 旁创建了额外文件。')
        report = directory / 'result.json'
        subprocess.run([str(directory / (variant.slug + '.exe')), '--package-smoke', str(report), str(media)],
                       cwd=directory, check=True, timeout=90)
        evidence = json.loads(report.read_text(encoding='utf-8'))
        if evidence.get('ok') is not True:
            raise SystemExit('Windows 包启动验收未通过。')
        evidence['portable'] = portable_evidence
        output = root / 'build' / 'windows-package-smoke.json'
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(evidence, indent=2) + '\n', encoding='utf-8')
        print(json.dumps(evidence))


if __name__ == '__main__':
    main()
