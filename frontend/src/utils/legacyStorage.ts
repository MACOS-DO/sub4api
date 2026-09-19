// Copy legacy preferences once; an explicit new value always wins.
export function migrateLegacyStorage(storage: Storage): void {
  for (const key of Object.keys(storage)) {
    if (!key.startsWith('sub2api_')) continue
    const target = key.replace(/^sub2api_/, 'sub4api_')
    if (storage.getItem(target) === null) {
      const value = storage.getItem(key)
      if (value !== null) storage.setItem(target, value)
    }
  }
}

// Storage can be unavailable in privacy-restricted browsers.
try {
  migrateLegacyStorage(localStorage)
  migrateLegacyStorage(sessionStorage)
} catch { /* Preferences fall back to normal defaults. */ }
