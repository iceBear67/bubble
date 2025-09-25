#!/usr/bin/env bash
# THIS SCRIPT IS A CLIENT OF THE MANAGEMENT SERVER
# WHICH IS USED FOR MANAGING THE CURRENT CONTAINER INSIDE ITSELF.

GATEWAY=${GATEWAY:-"$(ip -j route | jq -r '.[] | select(.gateway != null) | .gateway')"}
PORT=${PORT:-7684}

if test -z "$GATEWAY"; then
  echo "Did not find a valid gateway. Please explicitly set GATEWAY in environment(should be an IP)."
  echo "(tips: also make sure iproute2 and jq installed.)"
  exit 1
fi

function send_signal(){
  if test -z $1; then
    echo "Signal cannot be empty."
    return
  fi
  echo "Sending signal $1 to manager..."
  curl -X $1 "http://$GATEWAY:$PORT/$2"
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
  "expose")
    if test ! -z $2; then
      echo "expose <hostPort> <toPort>"
      return
    elif test ! -z $3; then
      echo "expose <hostPort> <toPort>"
      return
    fi
    send_signal "PORT" "from=$2&to=$3"
  ;;
  *)
    echo "Usage: bubble <destroy|stop|kill|expose>"
    echo "  For port forwarding: expose <hostPort> <toPort>"
    echo "  Port forwarding must be explicitly enabled in daemon config."
  ;;
esac