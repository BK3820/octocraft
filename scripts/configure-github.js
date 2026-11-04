const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const repo = process.env.REPO || 'StrawHat-Enterprise/github-app-factory';
const reviewer = process.env.REVIEWER || 'BK3820';
function gh(args, input) {
  return execFileSync('gh', args, { encoding: 'utf8', input, stdio: ['pipe', 'pipe', 'inherit'] });
}
function api(route, method = 'GET', body) {
  const args = ['api', route, '-X', method];
  if (body) args.push('--input', '-');
  return JSON.parse(gh(args, body ? JSON.stringify(body) : undefined));
}

try {
  gh(['variable', 'set', 'APP_FACTORY_ENABLED', '-R', repo, '-b', 'false']);
  const user = api(`users/${reviewer}`);
  const environment = api(`repos/${repo}/environments/production`, 'PUT', {
    reviewers: [{ type: 'User', id: user.id }],
    // A sole owner must be able to approve a run they triggered; this is a deliberate POC policy.
    prevent_self_review: false,
    can_admins_bypass: false,
    deployment_branch_policy: { protected_branches: false, custom_branch_policies: true },
  });
  const reviewers = environment.protection_rules.find((rule) => rule.type === 'required_reviewers');
  if (!reviewers?.reviewers.some((entry) => entry.reviewer.id === user.id)) {
    throw new Error('GitHub did not persist the required production reviewer.');
  }
  const policies = api(`repos/${repo}/environments/production/deployment-branch-policies`).branch_policies;
  if (!policies.some((entry) => entry.name === 'main' && entry.type === 'branch')) {
    api(`repos/${repo}/environments/production/deployment-branch-policies`, 'POST', { name: 'main', type: 'branch' });
  }
  const ruleset = JSON.parse(fs.readFileSync(path.join(__dirname, '../.github/rulesets/main.json'), 'utf8'));
  const existing = api(`repos/${repo}/rulesets`).find((entry) => entry.name === ruleset.name);
  api(`repos/${repo}/rulesets${existing ? `/${existing.id}` : ''}`, existing ? 'PUT' : 'POST', ruleset);
  api(`repos/${repo}`, 'PATCH', {
    allow_merge_commit: false, allow_rebase_merge: false, allow_squash_merge: true,
    delete_branch_on_merge: true, allow_auto_merge: false,
  });
  console.log(`Production requires approval by ${reviewer}; main ruleset installed. Deployment stays disabled.`);
} catch (error) {
  console.error('GitHub governance configuration failed. APP_FACTORY_ENABLED stays false.');
  console.error('Private repository required deployment reviewers need a supported Enterprise plan.');
  console.error(error.message);
  process.exitCode = 1;
}
