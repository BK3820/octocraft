const { test } = require('node:test');
const assert = require('node:assert/strict');
const { buildRequest } = require('../scripts/issue-to-request');

const ISSUE_BODY = `### App name

demo-bot

### Description

Demo

### Owner type

org

### Organization

my-org

### Homepage URL

https://example.com

### Webhook URL

_No response_

### Permissions

contents: read
issues: write
bogus line

### Webhook events

- [X] push
- [ ] issues

### Secret delivery

email

### Delivery email

a@example.com`;

test('IssueOps issue form body is converted to a manifest request', () => {
  const request = buildRequest({
    body: ISSUE_BODY,
    number: 7,
    html_url: 'https://github.com/o/r/issues/7',
    user: { login: 'u' },
  }, 'o/r');

  assert.equal(request.manifest.name, 'demo-bot');
  assert.deepEqual(request.manifest.default_permissions, { contents: 'read', issues: 'write' });
  assert.deepEqual(request.manifest.default_events, []);
  assert.equal(request.manifest.hook_attributes, undefined);
  assert.equal(request.secret_storage, 'email');
  assert.equal(request.source.issue_number, 7);
});
