const { defineConfig } = require('@playwright/test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gaf-browser-'));
process.env.UI_TEST_DATA_DIR = dataDir;

module.exports = defineConfig({
  testDir: './test/browser',
  workers: 1,
  use: { baseURL: 'http://127.0.0.1:3100', viewport: { width: 1440, height: 1100 } },
  globalTeardown: './test/browser/cleanup.js',
  webServer: {
    command: 'node server.js',
    url: 'http://127.0.0.1:3100/api/health',
    reuseExistingServer: false,
    env: {
      PORT: '3100', DATA_DIR: dataDir, STORAGE_ACCOUNT_NAME: '',
      KEY_VAULT_URL: '', VAULT_ADDR: '', VAULT_TOKEN: '',
      INGEST_TOKEN: 'browser-test-only', ALLOW_SIMULATE: 'true', GITHUB_TOKEN: '',
    },
  },
});
