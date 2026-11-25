# The published engine contains the exact archive, headers and runtime libc.
FROM ghcr.io/openhoo/hoosaidthat-hoovda-engine@sha256:8f549cb6e50ee09dea21bc768a66d263214d25438c2f48cb8d76e0230fa4a38a AS louis
FROM docker.io/library/rust:1.96.0-slim-bookworm@sha256:4732ca96fd086cb9be682050c3f0176288eebaac2b80aa2bcefccfaf198e1950 AS compiler
COPY --from=louis /opt/hoovda-runtime/include/liblouis /louis/include/liblouis
COPY --from=louis /opt/hoovda-runtime/lib/liblouis.a /louis/lib/liblouis.a
COPY --from=louis /lib/x86_64-linux-gnu/libc.so.6 /louis/libc.so.6
COPY --from=louis /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 /louis/ld-linux-x86-64.so.2
COPY main.c compile.sh /src/
RUN sh /src/compile.sh
FROM scratch AS asset
COPY --from=compiler /out/hoovda-braille-map /hoovda-braille-map-linux-amd64
