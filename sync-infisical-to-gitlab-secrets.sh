#!/usr/bin/env bash
# Copy this project's CI values from Infisical into its GitLab CI/CD variables.
#
# Infisical is the only place a value is edited; GitLab gets copies. Run it after changing a value in
# Infisical, or when the project's variables are gone (a new GitLab instance, a restored project):
#
#   ./sync-infisical-to-gitlab-secrets.sh --dry-run    # show what would change, change nothing
#   ./sync-infisical-to-gitlab-secrets.sh              # apply
#
# Values are never printed and never put on a command line: each request body goes through a 0600
# temporary file. Every variable written carries the description "managed by ci-sync (Infisical <path>)",
# and PRUNE=1 deletes only variables carrying that marker - so removing a line from VARS below is how you
# delete a variable, and a hand-made variable is never touched.
#
# Needs: infisical (logged in: `infisical login --domain "$INFISICAL_DOMAIN"`), glab (logged in to
# GITLAB_TARGET_HOST, with api scope), jq.
set -euo pipefail

# ---- this project -----------------------------------------------------------------------------------
GITLAB_TARGET_HOST=gitlab.com
GITLAB_PROJECT=radutoader/devops/kube-dns-checker
INFISICAL_DOMAIN=https://sm.radut.info
INFISICAL_ORG=471b91c6-6908-4632-ae56-06be289f8cf8
INFISICAL_PROJECT=d2b5a9f1-867a-4912-aba2-1e844062c29a
INFISICAL_ENV=prod
PRUNE=1

# infisical path <TAB> infisical key <TAB> gitlab variable <TAB> masked <TAB> protected <TAB> type
# type: env_var (default when the column is empty) or file. A file variable is written to a temp
# file by the runner and $VAR holds its PATH - which is why an SSH private key must be one; it is
# also multiline, and GitLab only masks single-line values, so masked is forced false for files.
VARS=$(cat <<'TSV'
# The Docker Hub push credentials, the only values this project needs: its image goes to
# docker.io/radut/kube-dns-checker and to this project's own GitLab registry (which the job token
# covers). No ARGOCD_DEPLOY_KEY row - this project has no deploy job; nothing in the estate pulls
# the image into a cluster from a manifest.
# PROTECTED (the 5th column): only pipelines on a protected ref see these. master is protected and the
# v* tag pattern was protected alongside this change, which is every ref that should publish to Docker
# Hub. A feature-branch pipeline cannot read the token at all, and still publishes to this project's
# own GitLab registry through the job token. Unlike slack-blocks and mikrotik-unifi-sync, where the
# same rows are unprotected because those projects have no protected tag pattern, so protecting them
# would break a release the moment it was tagged.
/registry/dockerhub-push	DOCKERHUB_USER	DOCKERIO_USER	false	true
/registry/dockerhub-push	DOCKERHUB_TOKEN	DOCKERIO_PASSWORD	true	true
TSV
)
# -----------------------------------------------------------------------------------------------------

DRY=0; [ "${1:-}" = "--dry-run" ] && DRY=1
for c in infisical glab jq; do command -v "$c" >/dev/null || { echo "missing: $c" >&2; exit 1; }; done

INF=(--domain "$INFISICAL_DOMAIN" --org "$INFISICAL_ORG" --projectId "$INFISICAL_PROJECT" --env "$INFISICAL_ENV" --silent)
GL=(glab api --hostname "$GITLAB_TARGET_HOST")       # --hostname: glab otherwise picks the host of the
PROJ=${GITLAB_PROJECT//\//%2F}                       # current directory's git remote
MARK="managed by ci-sync"
tmp=$(mktemp -d); chmod 700 "$tmp"; trap 'rm -rf "$tmp"' EXIT

"${GL[@]}" "projects/$PROJ/variables?per_page=100" > "$tmp/have.json"
jq -e 'type=="array"' "$tmp/have.json" >/dev/null || { echo "cannot list variables of $GITLAB_PROJECT on $GITLAB_TARGET_HOST" >&2; exit 1; }

rc=0; wanted=()
while IFS=$'\t' read -r path key var masked protected vtype; do
  vtype=${vtype:-env_var}
  [ -n "${path:-}" ] || continue
  case "$path" in \#*) continue ;; esac
  wanted+=("$var")

  if ! value=$(infisical secrets get "$key" "${INF[@]}" --path "$path" --plain </dev/null 2>"$tmp/err"); then
    echo "$var: cannot read Infisical $path $key" >&2
    grep -vE '^[[:space:]]*$' "$tmp/err" | head -2 | cut -c1-140 | sed 's/^/    /' >&2; rc=1; continue
  fi
  case "$value" in
    ""|CHANGE_ME*|REPLACE_ME*|TODO|'>>>>'*|'<<<<'*)
      echo "$var: Infisical $path $key holds a placeholder; set the real value first" >&2; rc=1; continue ;;
  esac
  # GitLab masks only single-line values of 8+ characters; asking for more is rejected
  [ "$masked" = true ] && [ "$vtype" != file ] && [ "${#value}" -ge 8 ] && [ "$value" = "${value%%$'\n'*}" ] && m=true || m=false

  desc="$MARK (Infisical $path)"
  # The value goes to jq through a 0600 file, never on a command line. `jq --arg v "$value"` would put the
  # secret in this process's argv, which any other local user can read from /proc/<pid>/cmdline for as long
  # as the call runs - and these are Docker Hub tokens and SSH private keys. Same reason the request body
  # is a file rather than a --data argument. (Flagged by the automated security review, 2026-11-11; the
  # other copies of this script in the estate still have the argv form.)
  printf '%s' "$value" > "$tmp/value"; chmod 600 "$tmp/value"
  jq -n --rawfile v "$tmp/value" --arg m "$m" --arg p "$protected" --arg d "$desc" --arg t "$vtype" \
     '{value:$v, masked:$m, protected:$p, description:$d, variable_type:$t}' > "$tmp/body.json"
  chmod 600 "$tmp/body.json"

  if jq -e --arg k "$var" --rawfile v "$tmp/value" --arg m "$m" --arg p "$protected" --arg d "$desc" --arg t "$vtype" \
      'map(select(.key==$k and .value==$v and (.masked|tostring)==$m and (.protected|tostring)==$p
                  and ((.variable_type // "env_var"))==$t and ((.description // ""))==$d)) | length==1' "$tmp/have.json" >/dev/null; then
    printf '%-28s ok\n' "$var"; continue
  fi

  if jq -e --arg k "$var" 'map(select(.key==$k)) | length==1' "$tmp/have.json" >/dev/null; then
    printf '%-28s update   masked=%s protected=%s type=%s\n' "$var" "$m" "$protected" "$vtype"
    [ "$DRY" = 1 ] || "${GL[@]}" --method PUT -H "Content-Type: application/json" \
      --input "$tmp/body.json" "projects/$PROJ/variables/$var" >/dev/null
  else
    printf '%-28s create   masked=%s protected=%s type=%s\n' "$var" "$m" "$protected" "$vtype"
    jq --arg k "$var" '. + {key:$k}' "$tmp/body.json" > "$tmp/post.json"; chmod 600 "$tmp/post.json"
    [ "$DRY" = 1 ] || "${GL[@]}" --method POST -H "Content-Type: application/json" \
      --input "$tmp/post.json" "projects/$PROJ/variables" >/dev/null
  fi
done <<< "$VARS"

if [ "$PRUNE" = 1 ]; then
  keep=$(printf '%s\n' "${wanted[@]:-}" | jq -R . | jq -s .)
  jq -r --argjson keep "$keep" --arg mark "$MARK" \
     '.[] | select((.description // "") | startswith($mark)) | select(.key as $k | $keep | index($k) | not) | .key' \
     "$tmp/have.json" | while read -r k; do
    [ -n "$k" ] || continue
    printf '%-28s delete   (no longer in VARS)\n' "$k"
    [ "$DRY" = 1 ] || "${GL[@]}" --method DELETE "projects/$PROJ/variables/$k" >/dev/null
  done
fi

[ "$DRY" = 1 ] && echo "(dry run - nothing was written)"
exit $rc
