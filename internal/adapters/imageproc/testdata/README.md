# imageproc test fixtures

Three small synthetic images, chosen to cover the shapes the resize and
thumbnail logic branches on. Their dimensions and formats are asserted in
`TestTestdataFixtures`, so a fixture that drifts fails loudly rather than
silently changing what the other tests mean.

| File                 | Size  | Format | Content                                     |
| -------------------- | ----- | ------ | ------------------------------------------- |
| `gradient_64x48.png` | 64x48 | PNG    | Landscape RGB gradient                      |
| `checker_48x64.jpg`  | 48x64 | JPEG   | Portrait checkerboard, quality 90           |
| `alpha_32x32.png`    | 32x32 | PNG    | Square, opaque centre with a clear border   |

They are generated, not photographic: every pixel comes from a formula, so they
compress to a few hundred bytes each and carry no metadata or licensing.
