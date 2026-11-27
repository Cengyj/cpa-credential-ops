#!/usr/bin/env python3
"""Reproduce the recorded official CPA source without changing its baseline."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import tarfile
import tempfile
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
COMMIT = '7df56074b252384d878ec4fca840552c6fb5236e'
URL = 'https://codeload.github.com/router-for-me/CLIProxyAPI/tar.gz/' + COMMIT
ARCHIVE_ROOT = 'CLIProxyAPI-' + COMMIT


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify_existing(destination, expected):
    paths = list(destination.rglob('*'))
    actual = {path.relative_to(destination).as_posix() for path in paths if path.is_file()}
    if destination.is_symlink() or any(path.is_symlink() for path in paths) or actual != set(expected):
        raise ValueError('existing official checkout has unexpected or missing files')
    for name, checksum in expected.items():
        if digest(destination / name) != checksum:
            raise ValueError('existing official source baseline mismatch: ' + name)


def prepare(archive, destination, expected):
    for name in expected:
        path = PurePosixPath(name)
        if path.is_absolute() or '..' in path.parts or path.as_posix() != name:
            raise ValueError('unsafe baseline path: ' + name)
    if destination.exists() or destination.is_symlink():
        verify_existing(destination, expected)
        return
    destination.parent.mkdir(parents=True, exist_ok=True)
    staged = Path(tempfile.mkdtemp(prefix='.official-source-', dir=destination.parent))
    try:
        with tarfile.open(archive, 'r:gz') as source:
            members = {}
            for entry in source.getmembers():
                prefix = ARCHIVE_ROOT + '/'
                if not entry.name.startswith(prefix):
                    continue
                name = entry.name[len(prefix):]
                if name not in expected:
                    continue
                if name in members or not entry.isfile():
                    raise ValueError('duplicate or non-regular official source: ' + name)
                members[name] = entry
            if set(members) != set(expected):
                raise ValueError('official archive is missing recorded source files')
            for name, checksum in expected.items():
                data = source.extractfile(members[name]).read()
                if hashlib.sha256(data).hexdigest() != checksum:
                    raise ValueError('official source baseline mismatch: ' + name)
                target = staged / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(data)
                target.chmod(0o755 if members[name].mode & 0o111 else 0o644)
        os.rename(staged, destination)
    finally:
        if staged.exists():
            shutil.rmtree(staged)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', type=Path, help='Use an already downloaded pinned source archive')
    parser.add_argument('--destination', type=Path, default=ROOT / 'workspaces/CLIProxyAPI-official')
    args = parser.parse_args()
    baseline = ROOT / 'host-patches/official-8.0.76-baseline.json'
    expected = json.loads(baseline.read_text())['sha256']
    destination = args.destination.absolute()
    if args.archive:
        prepare(args.archive, destination, expected)
    elif destination.exists() or destination.is_symlink():
        verify_existing(destination, expected)
    else:
        with tempfile.TemporaryDirectory(prefix='official-cpa-download-') as scratch:
            archive = Path(scratch) / 'source.tar.gz'
            with urlopen(URL, timeout=60) as response, archive.open('wb') as output:
                shutil.copyfileobj(response, output)
            prepare(archive, destination, expected)
    print(json.dumps({'official_version': '8.0.76', 'upstream_commit': COMMIT,
                      'verified_files': len(expected), 'baseline_sha256': digest(baseline),
                      'destination': str(destination)}))


if __name__ == '__main__':
    main()
