#!/usr/bin/env python3
"""Create portable release archives with explicit Unix executable permissions."""
from pathlib import Path
import hashlib
import sys
import tarfile
import zipfile


def archive_release(stage: Path, output: Path) -> None:
    if output.suffix == '.zip':
        with zipfile.ZipFile(output, 'w', zipfile.ZIP_DEFLATED) as archive:
            for path in sorted(stage.rglob('*')):
                if path.is_file():
                    archive.write(path, path.relative_to(stage))
        return

    def permissions(info: tarfile.TarInfo) -> tarfile.TarInfo:
        name = info.name.removeprefix('./')
        executable = name in ('sub4api', 'sub2api') or name.startswith('deploy/') and name.endswith('.sh')
        info.mode = 0o755 if info.isdir() or executable else 0o644
        info.uid = info.gid = 0
        info.uname = info.gname = 'root'
        return info

    with tarfile.open(output, 'w:gz', compresslevel=6) as archive:
        archive.add(stage, arcname='.', filter=permissions)


def write_checksums(directory: Path) -> None:
    archives = sorted([*directory.glob('*.tar.gz'), *directory.glob('*.zip')])
    lines = []
    for path in archives:
        with path.open('rb') as stream:
            digest = hashlib.file_digest(stream, 'sha256').hexdigest()
        lines.append(f'{digest}  {path.name}\n')
    (directory / 'checksums.txt').write_text(''.join(lines), encoding='utf-8', newline='\n')


if __name__ == '__main__':
    if sys.argv[1] == '--checksums':
        write_checksums(Path(sys.argv[2]))
    else:
        archive_release(Path(sys.argv[1]), Path(sys.argv[2]))
