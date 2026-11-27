import hashlib
import io
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import fetch_official_source as source


class OfficialSourceTests(unittest.TestCase):
    def setUp(self):
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name)
        self.destination = self.root / 'workspaces/official'
        self.archive = self.root / 'source.tar.gz'
        self.data = {'go.mod': b'module fixture\n', 'sdk/example.go': b'package sdk\n'}
        self.expected = {name: hashlib.sha256(data).hexdigest() for name, data in self.data.items()}

    def write_archive(self, entries=None, symlink=None):
        with tarfile.open(self.archive, 'w:gz') as archive:
            for name, data in (entries or list(self.data.items())):
                entry = tarfile.TarInfo(source.ARCHIVE_ROOT + '/' + name)
                if name == symlink:
                    entry.type = tarfile.SYMTYPE
                    entry.linkname = '/outside'
                    archive.addfile(entry)
                else:
                    entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))

    def test_verified_baseline_only_and_repeat_preserves_existing_files(self):
        self.write_archive([*self.data.items(), ('config.yaml', b'not source'), ('../escape', b'no')])
        source.prepare(self.archive, self.destination, self.expected)
        source.prepare(self.archive, self.destination, self.expected)
        actual = {p.relative_to(self.destination).as_posix() for p in self.destination.rglob('*') if p.is_file()}
        self.assertEqual(actual, set(self.expected))
        self.assertFalse((self.destination.parent / 'escape').exists())
        self.assertEqual((self.destination / 'go.mod').read_bytes(), self.data['go.mod'])

    def test_changed_archive_never_installs_partial_checkout(self):
        self.write_archive([('go.mod', b'changed'), ('sdk/example.go', self.data['sdk/example.go'])])
        with self.assertRaisesRegex(ValueError, 'baseline mismatch'):
            source.prepare(self.archive, self.destination, self.expected)
        self.assertFalse(self.destination.exists())
        self.assertEqual(list(self.destination.parent.iterdir()), [])

    def test_missing_duplicate_and_symlink_members_are_rejected(self):
        for entries, symlink in [([('go.mod', self.data['go.mod'])], None),
                                 ([*self.data.items(), ('go.mod', self.data['go.mod'])], None),
                                 (list(self.data.items()), 'go.mod')]:
            with self.subTest(entries=entries, symlink=symlink):
                self.write_archive(entries, symlink)
                with self.assertRaises(ValueError):
                    source.prepare(self.archive, self.destination, self.expected)
                self.assertFalse(self.destination.exists())

    def test_existing_edited_or_extra_source_is_never_replaced(self):
        self.write_archive()
        source.prepare(self.archive, self.destination, self.expected)
        (self.destination / 'go.mod').write_bytes(b'local edit')
        with self.assertRaisesRegex(ValueError, 'baseline mismatch'):
            source.prepare(self.archive, self.destination, self.expected)
        self.assertEqual((self.destination / 'go.mod').read_bytes(), b'local edit')
        (self.destination / 'go.mod').write_bytes(self.data['go.mod'])
        (self.destination / 'extra').write_bytes(b'extra')
        with self.assertRaisesRegex(ValueError, 'unexpected or missing'):
            source.prepare(self.archive, self.destination, self.expected)

    def test_unsafe_baseline_paths_are_rejected(self):
        for name in ('../escape', '/absolute', 'sdk/../escape', 'sdk//example.go'):
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, 'unsafe baseline'):
                source.prepare(self.archive, self.destination, {name: 'unused'})
        self.assertFalse(self.destination.exists())


if __name__ == '__main__':
    unittest.main()
