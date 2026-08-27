# Sourced, not executed: it exports into the calling shell.
#
#   . ../shared/load-env.sh .env
#
# Loads a .env so a key living only in that file still counts, without letting
# the file clobber a key exported in the shell.
#
# `set -a && . ./.env` on its own is not safe here. Both .env.example files
# ship empty `OPENAI_API_KEY=` and `ANTHROPIC_API_KEY=` lines, and
# `cp .env.example .env` is the documented setup, so sourcing applies those
# empties over a real exported key. The provider check then sees no key,
# selects the mock, and never merges the key the caller actually had.
#
# The shell wins on conflict, and an empty assignment from the file is dropped
# rather than left as an exported empty string, which is not the same as unset
# for anything testing with ${VAR:-}.

_load_env_file="${1:-.env}"

if [ -f "$_load_env_file" ]; then
  _shell_openai="${OPENAI_API_KEY:-}"
  _shell_anthropic="${ANTHROPIC_API_KEY:-}"
  _shell_pepper="${AEGIS_KEY_PEPPER:-}"

  set -a
  # shellcheck disable=SC1090
  . "$_load_env_file"
  set +a

  if [ -n "$_shell_openai" ]; then export OPENAI_API_KEY="$_shell_openai"; fi
  if [ -n "$_shell_anthropic" ]; then export ANTHROPIC_API_KEY="$_shell_anthropic"; fi
  if [ -n "$_shell_pepper" ]; then export AEGIS_KEY_PEPPER="$_shell_pepper"; fi

  [ -n "${OPENAI_API_KEY:-}" ] || unset OPENAI_API_KEY
  [ -n "${ANTHROPIC_API_KEY:-}" ] || unset ANTHROPIC_API_KEY
  [ -n "${AEGIS_KEY_PEPPER:-}" ] || unset AEGIS_KEY_PEPPER

  unset _shell_openai _shell_anthropic _shell_pepper
fi

unset _load_env_file
