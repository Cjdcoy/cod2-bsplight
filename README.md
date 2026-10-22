# cod2-bsplight

Finds and fixes Call of Duty 2 maps whose models render rainbow-colored.

## The cause

A map compiled without a light grid has BSP lumps 2 (grid entries) and 3
(grid colors) empty. The CoD2 renderer then lights models with its 8 debug
colors, so players, hands and weapons show up rainbow-colored.

## What `fix` does

- It inserts a 3-entry grid plus 1 color record, 48 bytes in total. The keys
  0, 0x80000000 and 0xFFFFFFFF keep the engine's nearest-entry binary search
  in range for any position. With 1 or 2 entries the search can read index -1.
- The color is the median of the grid samples from 333 compiled maps. The
  sun-visibility bit is set when worldspawn has sun keys.
- Only lumps 2 and 3 change. Later lump offsets shift by 48 bytes, and every
  other lump is verified byte-identical by hash. Other IWD entries are copied
  raw.
- Maps with a non-standard lump layout are skipped.

Limitation: the injected lighting is uniform across the whole map.

## Usage

```
bsplight scan <dir-with-iwds>    report the light grid state of every map
bsplight dump <map.iwd>          print every grid entry and color record
bsplight fix <srcdir> <outdir>   write fixed copies of IWDs with an empty grid
```

`fix` only reads `srcdir`. `outdir` must not be `srcdir` or inside it.

## For server admins

A fixed IWD has a new checksum, so players download each fixed map once.
Players fixing their own copies does not help on pure servers.

## Download or build

Windows and Linux binaries are on the
[Releases](https://github.com/Cjdcoy/cod2-bsplight/releases) page.
To build from source: `go build`.
