#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${REPO_URL:-git@github.com:nabutabu/crane-oss.git}"
WORKDIR="${WORKDIR:-/tmp/crane-oss-purge}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPLACEMENTS_FILE="${REPLACEMENTS_FILE:-${SCRIPT_DIR}/secrets-to-purge.txt}"
PUSH="${PUSH:-0}"

if [[ "${1:-}" == "--execute-push" ]]; then
  PUSH=1
fi

echo "This rewrites ALL branches of ${REPO_URL} to remove committed secrets."
echo "Prerequisites:"
echo "  1. Every exposed credential has been rotated/revoked in its provider (AWS, DB, tokens)."
echo "  2. All collaborators have stopped pushing; open PRs are closed or acknowledged."
echo "  3. You have a maintenance window and authority to force-push."
echo "  4. ${REPLACEMENTS_FILE} lists the values to purge, one 'literal==>replacement'"
echo "     per line (copy scripts/secrets-to-purge.example.txt and fill in real values)."
echo
echo "A mirror backup will be kept at ${WORKDIR%.git}.backup.git"
echo

if ! command -v git-filter-repo >/dev/null 2>&1; then
  echo "git-filter-repo is not installed."
  echo "Install with: pip install --user git-filter-repo   (or: pipx install git-filter-repo)"
  exit 1
fi

if [[ ! -f "${REPLACEMENTS_FILE}" ]]; then
  echo "ERROR: replacements file not found: ${REPLACEMENTS_FILE}"
  echo "Copy scripts/secrets-to-purge.example.txt to scripts/secrets-to-purge.txt"
  echo "and fill in the real values (one 'literal==>replacement' per line)."
  exit 1
fi

mapfile -t SECRET_PATTERNS < <(sed -e 's/==>.*//' -e '/^[[:space:]]*#/d' -e '/^[[:space:]]*$/d' "${REPLACEMENTS_FILE}")
if [[ ${#SECRET_PATTERNS[@]} -eq 0 ]]; then
  echo "ERROR: ${REPLACEMENTS_FILE} contains no patterns; aborting."
  exit 1
fi

for pattern in "${SECRET_PATTERNS[@]}"; do
  if [[ "${pattern}" == \<*\> ]]; then
    echo "ERROR: ${REPLACEMENTS_FILE} still contains the placeholder '${pattern}'."
    echo "Replace every <PLACEHOLDER> with the real value before running."
    exit 1
  fi
done

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

echo "==> Rewriting history for:"
printf '    %s\n' "${SECRET_PATTERNS[@]}"

git -C "${WORKDIR}" filter-repo --replace-text "${REPLACEMENTS_FILE}" --force

echo
echo "==> Verifying no target literals remain in any ref"
GREP_ARGS=()
for pattern in "${SECRET_PATTERNS[@]}"; do
  GREP_ARGS+=(-e "${pattern}")
done
if git -C "${WORKDIR}" grep -n -I "${GREP_ARGS[@]}" $(git -C "${WORKDIR}" rev-list --all) 2>/dev/null; then
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
