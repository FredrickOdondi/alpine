#!/bin/sh
# Fake gate tool that hangs, with a child process holding stdout open.
echo "starting"
sleep 30 &
sleep 30
