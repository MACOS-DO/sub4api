#!/usr/bin/env python3
"""Render maintained release notes with installation instructions."""
import os
from pathlib import Path

version = os.environ['VERSION']
repo = os.environ['GITHUB_REPOSITORY']
ghcr = os.environ['GHCR_IMAGE']
hub = os.environ['DOCKERHUB_IMAGE']
notes = Path(f'changelog/{version}/CHANGELOG.md').read_text().strip()
if notes.startswith('# '):
    notes = notes.split('\n', 1)[1].strip()
if not notes:
    raise SystemExit('Release notes must contain a body')
body = f'''> Sub4API - AI API Gateway Platform · 将 AI 订阅配额分发和管理

{notes}

---

## 📥 Installation

**Docker (amd64 / arm64):**
```bash
# Docker Hub
docker pull {hub}:{version}

# GitHub Container Registry
docker pull {ghcr}:{version}
```

**One-line install (Linux):**
```bash
curl -fsSL https://raw.githubusercontent.com/{repo}/main/deploy/install.sh | sudo bash
```

**Manual download:**
下载下方对应平台的 `sub4api` 安装包，使用 `checksums.txt` 校验完整性。
`sub2api` 同名兼容包供旧版更新器使用。

## 📚 Documentation

- [GitHub Repository](https://github.com/{repo})
- [Installation Guide](https://github.com/{repo}/blob/main/deploy/README.md)

<details>
<summary>Build information</summary>

- Commit: `{os.environ['COMMIT']}`
- GHCR: `{ghcr}@{os.environ['IMAGE_DIGEST']}`
- Docker Hub: `{hub}@{os.environ['IMAGE_DIGEST']}`

</details>
'''
Path('dist/release-notes.md').write_text(body)
