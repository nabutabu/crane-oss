#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${REPO_URL:-git@github.com:nabutabu/crane-oss.git}"
WORKDIR="${WORKDIR:-/tmp/crane-oss-purge}"
PUSH="${PUSH:-0}"

if [[ "${1:-}" == "--execute-push" ]]; then
  PUSH=1
fi

echo "This rewrites ALL branches of ${REPO_URL} to remove committed secrets."
echo "Prerequisites:"
echo "  1. Every exposed credential has been rotated/revoked in its provider (AWS, DB, tokens)."
echo "  2. All collaborators have stopped pushing; open PRs are closed or acknowledged."
echo "  3. You have a maintenance window and authority to force-push."
echo
echo "A mirror backup will be kept at ${WORKDIR%.git}.backup.git"
echo

if ! command -v git-filter-repo >/dev/null 2>&1; then
  echo "git-filter-repo is not installed."
  echo "Install with: pip install --user git-filter-repo   (or: pipx install git-filter-repo)"
  exit 1
fi

if [[ -e "${WORKDIR}" ]]; then
  echo "Removing previous work dir ${WORKDIR}"
  rm -rf "${WORKDIR}"
fi

echo "==> Mirror-cloning ${REPO_URL}"
git clone --mirror "${REPO_URL}" "${WORKDIR}"

BACKUP="${WORKDIR%.git}.backup.git"
if [[ -e "${BACKUP}" ]]; then
  rm -rf "${BACKUP}"
fi
cp -a "${WORKDIR}" "${BACKUP}"
echo "==> Backup written to ${BACKUP}"

REPLACEMENTS="$(mktemp)"
trap 'rm -f "${REPLACEMENTS}"' EXIT
cat >"${REPLACEMENTS}" <<'EOF'
AKIA5JBCYANFBNA6VFE7==>REDACTED_AWS_ACCESS_KEY_ID
AKIA5JBCYANFCGBO7IEV==>REDACTED_AWS_ACCESS_KEY_ID
mysecretpassword==>REDACTED_DB_PASSWORD
secret-token==>REDACTED_TOKEN
EOF

echo "==> Rewriting history for:"
sed 's/==>.*//' "${REPLACEMENTS}" | sed 's/^/    /'

git -C "${WORKDIR}" filter-repo --replace-text "${REPLACEMENTS}" --force

echo
echo "==> Verifying no target literals remain in any ref"
if git -C "${WORKDIR}" grep -n -I -e 'AKIA5JBCYANFBNA6VFE7' -e 'AKIA5JBCYANFCGBO7IEV' -e 'mysecretpassword' -e 'secret-token' $(git -C "${WORKDIR}" rev-list --all) 2>/dev/null; then
  echo "ERROR: literals still present; aborting."
  exit 1
fi
echo "    clean"

if [[ "${PUSH}" != "1" ]]; then
  echo
  echo "Dry run complete. Nothing was pushed."
  echo "To publish the rewritten history (destructive, coordinated):"
  echo "  git -C ${WORKDIR} remote set-url origin ${REPO_URL}"
  echo "  git -C ${WORKDIR} push --force --all"
  echo "  git -C ${WORKDIR} push --force --tags"
  echo "  # Then tell every collaborator to re-clone; discard stale clones."
  echo
  echo "Re-run with --execute-push to attempt the push from this script."
  exit 0
fi

echo "==> Force-pushing all refs to origin"
git -C "${WORKDIR}" remote set-url origin "${REPO_URL}"
git -C "${WORKDIR}" push --force --all
git -C "${WORKDIR}" push --force --tags

echo
echo "Done. Notify all collaborators to re-clone. Rotation of every secret is still required."
echo "Backup retained at ${BACKUP}"
