#!/usr/bin/env python3
"""Prepare a verified source-only Docker context without copying runtime data."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
from release_support import source_candidates

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def prepare(destination):
    if destination.exists():
        raise ValueError('refusing to replace an existing build context')
    baseline = json.loads((ROOT / 'host-patches/official-8.0.76-baseline.json').read_text())
    source = ROOT / 'workspaces/CLIProxyAPI-official'
    for relative, expected in baseline['sha256'].items():
        path = source / relative
        if path.is_symlink() or not path.is_file() or digest(path) != expected:
            raise ValueError('official source baseline mismatch: ' + relative)
    destination.mkdir(parents=True)
    for relative in baseline['sha256']:
        target = destination / 'host' / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source / relative, target)
    for directory in ('plugin', 'login-runtime-go'):
        for path in source_candidates(ROOT / directory, {'payload', '__pycache__', 'node_modules', '.git'}):
            relative = path.relative_to(ROOT)
            if path.is_symlink() or not path.is_file():
                continue
            if path.suffix not in {'.go', '.mod', '.sum', '.json', '.js', '.css', '.html', '.py', '.md', '.txt'} and path.name not in {'LICENSE', 'NOTICE'}:
                continue
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
    for name, target in (('scripts/build_official_plugin.py', 'scripts/build_official_plugin.py'),
                         ('deploy/docker/build_plugin.py', 'build_plugin.py'),
                         ('deploy/docker/Dockerfile', 'Dockerfile')):
        (destination / target).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(ROOT / name, destination / target)
    manifest = {'unchanged_host_files': len(baseline['sha256']), 'host_baseline': 'official-8.0.76-baseline.json',
                'source_sha256': {str(p.relative_to(destination)): digest(p) for p in sorted(destination.rglob('*')) if p.is_file()}}
    (destination.parent / (destination.name + '-manifest.json')).write_text(json.dumps(manifest, indent=2) + '\n')
    print(destination)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('destination', type=Path)
    prepare(parser.parse_args().destination.resolve())
