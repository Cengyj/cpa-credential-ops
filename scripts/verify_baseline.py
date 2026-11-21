#!/usr/bin/env python3
"""Verify the actual source archive without reading excluded runtime directories."""
from pathlib import Path
import argparse, hashlib, json
root=Path(__file__).resolve().parents[1]
p=argparse.ArgumentParser();p.add_argument('--reference',action='store_true');p.add_argument('--source',type=Path);p.add_argument('--manifest',type=Path,default=root/'host-patches/official-8.0.76-baseline.json');a=p.parse_args()
m=json.loads(a.manifest.read_text())
source=Path(m['source']) if a.reference else a.source
if source is None:p.error('choose --reference or --source <unmodified-source>')
fail=[]
for rel,expected in m['sha256'].items():
    file=source/rel
    if not file.is_file() or hashlib.sha256(file.read_bytes()).hexdigest()!=expected:fail.append(rel)
if fail:raise SystemExit('Baseline mismatch: '+', '.join(fail))
print(f'Verified {len(m["sha256"])} unmodified baseline source/build files at {source}')
