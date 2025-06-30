#!/usr/bin/env bash
# THIS SCRIPT IS A CLIENT OF THE MANAGEMENT SERVER
# WHICH IS USED FOR MANAGING THE CURRENT CONTAINER INSIDE ITSELF.

if test -z "$BUBBLE_SOCK"; then
  echo "BUBBLE_SOCK is not present! Are you in a managed container?"
  echo "Falling back to /mnt/data/daemon.sock as default."
  BUBBLE_SOCK="/mnt/data/daemon.sock"
fi

if test ! -x $BUBBLE_SOCK; then
  echo "You don't have access to $BUBBLE_SOCK."
  echo "tip: Try again with sudo prefix."
  exit 1
fi

function send_signal(){
  if test -z $1; then
    echo "Signal cannot be empty."
    return
  fi
  echo "Sending signal $1 to manager..."
  curl --unix-socket $BUBBLE_SOCK -X $1 http://localhost
}

case "$1" in
  "stop")
    send_signal "STOP"
  ;;
  "destroy")
    send_signal "DESTROY"
  ;;
  "kill")
    send_signal "KILL"
  ;;
  *)
    echo "Usage: bubble <destroy|stop|kill>"
  ;;
esac