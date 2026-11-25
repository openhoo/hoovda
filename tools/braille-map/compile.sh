#!/bin/sh
# Both image and standalone builds run this with the same pinned compiler.
set -eu
mkdir -p /out
cc -std=c11 -O2 -Wall -Wextra -Werror -fstack-protector-strong -fPIE -pie \
  -I/louis/include /src/main.c /louis/lib/liblouis.a \
  /louis/libc.so.6 /louis/ld-linux-x86-64.so.2 -Wl,--build-id=none,-z,relro,-z,now -s -o /out/hoovda-braille-map
