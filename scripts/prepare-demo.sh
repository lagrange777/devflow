#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ -e demo/hello ]; then
  echo 'demo/hello already exists; refusing to overwrite it.' >&2
  exit 1
fi
mkdir -p demo/hello
cp examples/hello/go.mod examples/hello/main.go examples/hello/main_test.go demo/hello/
git -C demo/hello init -b main
git -C demo/hello add .
git -C demo/hello -c core.hooksPath=/dev/null -c commit.gpgsign=false -c user.name=Devflow -c user.email=devflow@localhost commit -m 'Initial demo Go service'
echo 'Demo target is ready at demo/hello'
