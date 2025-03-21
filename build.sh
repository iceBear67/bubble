#!/usr/bin/env bash

for entry in *.go; do
  grep -q "func main()" $entry
  if [[ $? == 0 ]]; then
    echo "building $entry"
    CGO_ENABLED=0 go build -o target/$(basename -s .go $entry) $entry
  fi
done
