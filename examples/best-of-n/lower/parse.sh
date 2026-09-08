#!/usr/bin/env bash
# parse.sh — print the config keys, one per line.
# BUGGY as shipped: comment lines leak through as keys (see ticket.txt).
cut -d= -f1 "$1"
