// Posts or updates a single PR comment summarising CI job results.
// Invoked by ci.yml via actions/github-script (keeps JS out of YAML).
const needs = JSON.parse(process.env.NEEDS_JSON || '{}');
const ciSkipped = process.env.CI_SKIPPED === 'true';
const docsOnly = process.env.DOCS_ONLY === 'true';

function icon(r) {
  if (r === 'success') return '✅';
  if (r === 'skipped') return '⏭️';
  if (r === 'cancelled') return '🚫';
  return '❌';
}

const coveragePct = needs['validate-test']?.outputs?.pct;
const threshold = needs['validate-test']?.outputs?.threshold || '?';
const coverageLabel = 'Coverage ≥ ' + threshold + '%';

const rows = [
  ['Tests & race detector', 'validate-test'],
  [coverageLabel,           'validate-test'],
  ['Trivy CVE scan',        'trivy'],
  ['Code quality',          'validate-quality'],
];

// Informational: an incompatible change warns here and blocks only at
// release (unless the release bumps the major version).
const infoRows = [
  ['API compatibility (informational)', 'api-compat'],
];

const table = rows.concat(infoRows)
  .map(function (row) {
    const label = row[0];
    const key   = row[1];
    const r     = needs[key]?.result || 'skipped';
    const value = (label === coverageLabel && coveragePct)
      ? parseFloat(coveragePct).toFixed(1) + '%'
      : '`' + r + '`';
    return '| ' + icon(r) + ' | ' + label + ' | ' + value + ' |';
  })
  .join('\n');

const allPassed = rows.every(function (row) {
  return needs[row[1]]?.result === 'success';
});
const headline = ciSkipped
  ? '⏭️ CI skipped'
  : allPassed
    ? '✅ All checks passed — ready to merge'
    : '❌ Some checks failed';

const skipNote = !ciSkipped
  ? '📦 Library module — no image to build; Trivy scans go.mod/go.sum. API changes since the last release: see the API compatibility job.'
  : docsOnly
    ? '_Documentation-only change — build/test jobs skipped; the Docs workflow checks the architecture diagrams._'
    : '_CI gate closed — this PR is a draft or labeled `skip-ci`. Mark it ready for review (or remove the label) to run the full suite._';

const marker = '<!-- ci-pr-summary -->';
const body = [
  marker,
  '## ' + headline,
  '',
  '| | Check | Result |',
  '|---|---|---|',
  table,
  '',
  skipNote,
  '🔍 [Security tab](https://github.com/' +
    context.repo.owner +
    '/' +
    context.repo.repo +
    '/security)',
].join('\n');

const { data: comments } = await github.rest.issues.listComments({
  ...context.repo,
  issue_number: context.issue.number,
});
const existing = comments.find(function (c) {
  return c.body?.startsWith(marker);
});

if (existing) {
  await github.rest.issues.updateComment({
    ...context.repo,
    comment_id: existing.id,
    body,
  });
} else {
  await github.rest.issues.createComment({
    ...context.repo,
    issue_number: context.issue.number,
    body,
  });
}
