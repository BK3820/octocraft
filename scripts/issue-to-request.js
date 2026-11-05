// Converts a GitHub issue-form body into a OctoCraft request payload.
// Used by .github/workflows/issueops-github-app.yml and testable locally:
//   node scripts/issue-to-request.js < sample-issue.md
const NONE = new Set(['', '_No response_', 'None']);

function parseSections(body) {
  const out = {};
  const parts = String(body || '').split(/^###\s+/m).slice(1);
  for (const part of parts) {
    const [heading, ...rest] = part.split('\n');
    const value = rest.join('\n').trim();
    out[heading.trim()] = NONE.has(value) ? '' : value;
  }
  return out;
}

function buildRequest(issue, repo) {
  const s = parseSections(issue.body);
  const permissions = {};
  for (const line of (s['Permissions'] || '').split('\n')) {
    const m = line.match(/^\s*([a-z_]+)\s*:\s*(read|write|admin)\s*$/i);
    if (m) permissions[m[1].toLowerCase()] = m[2].toLowerCase();
  }
  const events = (s['Webhook events'] || '')
    .split('\n')
    .map((l) => l.match(/^- \[x\]\s+(.+)$/i))
    .filter(Boolean)
    .map((m) => m[1].trim());
  const webhook = s['Webhook URL'];

  const manifest = {
    name: s['App name'],
    description: s['Description'],
    url: s['Homepage URL'],
    public: false,
    default_permissions: permissions,
    default_events: webhook ? events : [],
  };
  if (webhook) manifest.hook_attributes = { url: webhook, active: true };

  return {
    owner_type: s['Owner type'] || 'org',
    owner: s['Organization'],
    secret_storage: s['Secret delivery'] || 'vault',
    notify_email: s['Delivery email'] || undefined,
    requester: issue.user?.login,
    source: { type: 'issue', repo, issue_number: issue.number, url: issue.html_url },
    manifest,
  };
}

module.exports = { parseSections, buildRequest };

if (require.main === module) {
  let body = '';
  process.stdin.on('data', (c) => (body += c));
  process.stdin.on('end', () => {
    const issue = { body, number: 1, html_url: 'https://github.com/example/repo/issues/1', user: { login: 'local-user' } };
    console.log(JSON.stringify(buildRequest(issue, 'example/repo'), null, 2));
  });
}
