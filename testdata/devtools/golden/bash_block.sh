# >>> zatvor >>>
# Managed by Zatvor: while Zatvor is connected, new prompts route developer
# tools through it. Zatvor removes this block when the feature is turned off.
__zatvor_state='/c/Users/test/AppData/Local/Zatvor/Run/devtools.proxy'
__zatvor_last=
__zatvor_set=
__zatvor_sync() {
  local p=
  if [ -r "$__zatvor_state" ]; then IFS= read -r p < "$__zatvor_state" || :; fi
  p=${p%$'\r'}
  [ "$p" = "$__zatvor_last" ] && return 0
  __zatvor_last=$p
  if [ -n "$p" ]; then
    export HTTPS_PROXY="$p" HTTP_PROXY="$p" NO_PROXY='localhost,127.0.0.1,::1'
    __zatvor_set=$p
  elif [ -n "$__zatvor_set" ]; then
    [ "${HTTPS_PROXY-}" = "$__zatvor_set" ] && unset HTTPS_PROXY
    [ "${HTTP_PROXY-}" = "$__zatvor_set" ] && unset HTTP_PROXY
    [ "${NO_PROXY-}" = 'localhost,127.0.0.1,::1' ] && unset NO_PROXY
    __zatvor_set=
  fi
  return 0
}
case "${PROMPT_COMMAND-}" in
  *__zatvor_sync*) ;;
  *) PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND$'\n'}__zatvor_sync" ;;
esac
__zatvor_sync
# <<< zatvor <<<
