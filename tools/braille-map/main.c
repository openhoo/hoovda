/* SPDX-License-Identifier: Apache-2.0
 * Use the position arrays of one bounded lou_translate call. The Go engine
 * sends little-endian Unicode code points, independent of Liblouis's build
 * width. Each cell in the response refers to an original Go rune.
 */
#include <liblouis/liblouis.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_RUNES (1U << 20)
#define MAX_CELLS (2U << 20)
_Static_assert(sizeof(widechar) == 2 || sizeof(widechar) == 4, "Liblouis widechar size");
static int read32(uint32_t *n) {
    unsigned char b[4];
    if (fread(b, 1, 4, stdin) != 4)
        return 0;
    *n = (uint32_t)b[0] | ((uint32_t)b[1] << 8) | ((uint32_t)b[2] << 16) | ((uint32_t)b[3] << 24);
    return 1;
}
static int write32(uint32_t n) {
    unsigned char b[4] = {(unsigned char)n, (unsigned char)(n >> 8), (unsigned char)(n >> 16),
                          (unsigned char)(n >> 24)};
    return fwrite(b, 1, 4, stdout) == 4;
}
static int fail(const char *msg) {
    fprintf(stderr, "%s\n", msg);
    return 1;
}
int main(int argc, char **argv) {
    uint32_t nr;
    if (argc == 2 && !strcmp(argv[1], "--version")) {
        printf("hoovda-braille-map protocol 1; Liblouis %s\n", lou_version());
        return 0;
    }
    if (argc != 2)
        return fail("expected one supported table");
    const char *table = !strcmp(argv[1], "en-us-g2.ctb") ? "unicode.dis,en-us-g2.ctb"
                        : !strcmp(argv[1], "de-g2.ctb")  ? "unicode.dis,de-g2.ctb"
                                                         : NULL;
    if (!table || !read32(&nr) || nr < 1 || nr > MAX_RUNES ||
        (unsigned)lou_charSize() != sizeof(widechar))
        return fail("invalid mapping request");
    widechar *in = calloc(2 * (size_t)nr, sizeof(*in));
    uint32_t *first = calloc(nr, sizeof(*first)), *rune = calloc(2 * (size_t)nr, sizeof(*rune));
    if (!in || !first || !rune)
        return fail("input allocation failed");
    int ni = 0;
    for (uint32_t i = 0; i < nr; i++) {
        uint32_t cp;
        if (!read32(&cp) || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff))
            return fail("invalid Unicode scalar");
        first[i] = ni;
        if (sizeof(widechar) == 2 && cp > 0xffff) {
            uint32_t x = cp - 0x10000;
            rune[ni] = i;
            in[ni++] = (widechar)(0xd800 + (x >> 10));
            cp = 0xdc00 + (x & 1023);
        }
        rune[ni] = i;
        in[ni++] = (widechar)cp;
    }
    if (getc(stdin) != EOF || ferror(stdin))
        return fail("excess input");
    int *to_cell = malloc((size_t)ni * sizeof(int));
    if (!to_cell)
        return fail("input position allocation failed");
    uint32_t cap = (uint32_t)ni + 64;
    if (cap > MAX_CELLS)
        cap = MAX_CELLS;
    while (1) {
        widechar *out = calloc(cap, sizeof(*out));
        int *to_unit = malloc(cap * sizeof(int));
        if (!out || !to_unit)
            return fail("output allocation failed");
        memset(to_cell, 0xff, (size_t)ni * sizeof(int));
        memset(to_unit, 0xff, cap * sizeof(int));
        int consumed = ni, len = (int)cap;
        if (!lou_translate(table, in, &consumed, out, &len, NULL, NULL, to_cell, to_unit, NULL, 0))
            return fail("Liblouis translation failed");
        if (len < 0 || len > (int)cap || consumed < 0 || consumed > ni)
            return fail("invalid translation length");
        if (consumed != ni || len == (int)cap) {
            free(out);
            free(to_unit);
            if (cap == MAX_CELLS)
                return fail("braille translation exceeds cell limit");
            cap = cap > MAX_CELLS / 2 ? MAX_CELLS : cap * 2;
            continue;
        }
        if (!len)
            return fail("translation returned no display cells");
        for (int j = 0; j < len; j++)
            if (out[j] < 0x2800 || out[j] > 0x28ff || to_unit[j] < 0 || to_unit[j] >= ni)
                return fail("unmapped display cell");
        for (uint32_t i = 0; i < nr; i++)
            if (to_cell[first[i]] < 0 || to_cell[first[i]] >= len)
                return fail("unmapped input character");
        if (!write32((uint32_t)len) || !write32(nr))
            return fail("output failed");
        for (int j = 0; j < len; j++)
            if (putc((int)(out[j] - 0x2800), stdout) == EOF)
                return fail("output failed");
        for (int j = 0; j < len; j++)
            if (!write32(rune[to_unit[j]]))
                return fail("output failed");
        for (uint32_t i = 0; i < nr; i++)
            if (!write32((uint32_t)to_cell[first[i]]))
                return fail("output failed");
        return fflush(stdout) == 0 ? 0 : fail("output failed");
    }
}
