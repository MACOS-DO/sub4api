#!/usr/bin/env python3
"""Offline regression tests for version, notes and archive compatibility."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[2]

class ReleaseTest(unittest.TestCase):
    def test_fork_image_defaults_and_update_source(self):
        image = 'ghcr.io/info-talent/sub4api:latest'
        for name in ['docker-compose.yml', 'docker-compose.local.yml', 'docker-compose.standalone.yml']:
            text = (ROOT / 'deploy' / name).read_text(encoding='utf-8')
            self.assertIn('image: ${SUB4API_IMAGE:-' + image + '}', text)
            self.assertNotIn('weishaw/sub2api', text)
        self.assertIn('SUB4API_IMAGE=' + image, (ROOT / 'deploy/.env.example').read_text(encoding='utf-8'))
        for name in ['.goreleaser.yaml', '.goreleaser.simple.yaml']:
            text = (ROOT / name).read_text(encoding='utf-8')
            self.assertIn('project_name: sub4api', text)
            self.assertNotIn('/sub2api:', text)
        self.assertIn('githubRepo     = "info-talent/sub4api"',
                      (ROOT / 'backend/internal/service/update_service.go').read_text(encoding='utf-8'))

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for target in ['backend/scripts', 'backend/cmd/server', 'changelog/0.2.7', 'dist', 'deploy', 'backend/resources', 'bin']:
            (self.root / target).mkdir(parents=True)
        shutil.copy(ROOT / 'backend/scripts/resolve-version.sh', self.root / 'backend/scripts')
        (self.root / 'backend/cmd/server/VERSION').write_text('0.2.7\n')
        (self.root / 'changelog/0.2.7/CHANGELOG.md').write_text('# v0.2.7\n\n## 新增功能\n\n- 实际更新\n')
        (self.root / 'LICENSE').write_text('fixture')
        (self.root / 'README.md').write_text('fixture')
        self.env = {**os.environ, 'DOCKERHUB_USERNAME': 'Example', 'DOCKERHUB_TOKEN': 'test-only',
                    'GITHUB_REPOSITORY': 'info-talent/sub4api', 'GITHUB_OUTPUT': str(self.root / 'output'),
                    'VERSION': '0.2.7', 'COMMIT': 'abc123', 'BUILD_DATE': '2026-09-19T00:00:00Z',
                    'GHCR_IMAGE': 'ghcr.io/info-talent/sub4api', 'DOCKERHUB_IMAGE': 'example/sub4api',
                    'IMAGE_DIGEST': 'sha256:test'}
        self.git('init', '-q')
        self.git('add', '.')
        self.git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'fixture')

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.root, capture_output=True, check=True)

    def prepare(self):
        return subprocess.run(['bash', str(ROOT / 'scripts/release/prepare.sh')], cwd=self.root,
                              env=self.env, capture_output=True, text=True)

    def test_file_version_and_registry_names(self):
        result = self.prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        output = (self.root / 'output').read_text()
        self.assertIn('version=0.2.7\n', output)
        self.assertIn('ghcr=ghcr.io/info-talent/sub4api', output)
        self.assertIn('dockerhub=example/sub4api', output)

    def test_exact_tag_wins(self):
        self.git('tag', 'v0.2.8')
        (self.root / 'changelog/0.2.8').mkdir()
        (self.root / 'changelog/0.2.8/CHANGELOG.md').write_text('New version notes')
        result = self.prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('version=0.2.8', (self.root / 'output').read_text())

    def test_ghcr_only_release_needs_no_dockerhub_credentials(self):
        self.env.pop('DOCKERHUB_USERNAME')
        self.env.pop('DOCKERHUB_TOKEN')
        result = self.prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        output = (self.root / 'output').read_text()
        self.assertIn('dockerhub_enabled=false\n', output)
        self.assertIn('dockerhub=\n', output)
        self.assertIn('tags<<IMAGE_TAGS\nghcr.io/info-talent/sub4api:0.2.7\nghcr.io/info-talent/sub4api:latest\nIMAGE_TAGS', output)
        self.assertNotIn('example/sub4api', output)

    def test_partial_dockerhub_credentials_fail_early(self):
        for missing in ('DOCKERHUB_USERNAME', 'DOCKERHUB_TOKEN'):
            with self.subTest(missing=missing):
                original = self.env.pop(missing)
                try:
                    result = self.prepare()
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('Configure ' + missing, result.stderr)
                finally:
                    self.env[missing] = original

    def test_missing_notes_empty_body_and_credentials_stop_early(self):
        notes = self.root / 'changelog/0.2.7/CHANGELOG.md'
        for text in ['', '# v0.2.7\n', '# v0.2.7\n\n## 新增功能\n']:
            notes.write_text(text)
            self.assertNotEqual(self.prepare().returncode, 0)
        notes.unlink()
        self.assertNotEqual(self.prepare().returncode, 0)
        self.env['DOCKERHUB_TOKEN'] = ''
        self.assertIn('Configure DOCKERHUB_TOKEN', self.prepare().stderr)

    def test_notes_render(self):
        subprocess.run([sys.executable, str(ROOT / 'scripts/release/notes.py')], cwd=self.root, env=self.env, check=True)
        text = (self.root / 'dist/release-notes.md').read_text()
        for expected in ['实际更新', '## 📥 Installation', '## 📚 Documentation', 'info-talent/sub4api', 'abc123', 'sha256:test']:
            self.assertIn(expected, text)
        self.assertNotIn('Wei-Shaw', text)

    def test_ghcr_only_notes_omit_dockerhub(self):
        self.env['DOCKERHUB_IMAGE'] = ''
        subprocess.run([sys.executable, str(ROOT / 'scripts/release/notes.py')], cwd=self.root, env=self.env, check=True)
        text = (self.root / 'dist/release-notes.md').read_text()
        self.assertIn('docker pull ghcr.io/info-talent/sub4api:0.2.7', text)
        self.assertNotIn('Docker Hub', text)
        self.assertNotIn('docker pull :', text)

    def test_all_archives_contain_new_and_legacy_binary_and_checksums(self):
        # Stub only the compiler; exercise real archive and checksum commands.
        go = self.root / 'bin/go'
        go.write_text('#!/bin/sh\nwhile [ "$1" != "-o" ]; do shift; done\nshift\nprintf binary > "$1"\n')
        go.chmod(0o755)
        self.env['PATH'] = str(go.parent) + os.pathsep + os.environ['PATH']
        subprocess.run(['bash', str(ROOT / 'scripts/release/build-archives.sh')], cwd=self.root, env=self.env, check=True)
        output = self.root / 'dist/release'
        self.assertEqual(len(list(output.glob('*.tar.gz'))), 8)
        self.assertEqual(len(list(output.glob('*.zip'))), 2)
        checksum_lines = (output / 'checksums.txt').read_text().splitlines()
        self.assertEqual(len(checksum_lines), 10)
        for line in checksum_lines:
            self.assertRegex(line, r'^[0-9a-f]{64}  sub[24]api_')
        with tarfile.open(output / 'sub2api_0.2.7_linux_amd64.tar.gz') as archive:
            self.assertIn('./sub2api', archive.getnames())
            self.assertIn('./sub4api', archive.getnames())
            self.assertEqual(archive.getmember('./sub2api').mode, 0o755)
            self.assertEqual(archive.getmember('./sub4api').mode, 0o755)
            self.assertEqual(archive.getmember('./README.md').mode, 0o644)
        with zipfile.ZipFile(output / 'sub4api_0.2.7_windows_amd64.zip') as archive:
            self.assertIn('sub2api.exe', archive.namelist())
            self.assertIn('sub4api.exe', archive.namelist())
        subprocess.run(['sha256sum', '-c', 'checksums.txt'], cwd=output, check=True, stdout=subprocess.DEVNULL)

if __name__ == '__main__':
    unittest.main()
