const fs = require('node:fs');
module.exports = async () => {
  fs.rmSync(process.env.UI_TEST_DATA_DIR, { recursive: true, force: true });
};
