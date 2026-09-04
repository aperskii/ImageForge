# imageproc test fixtures

Small synthetic images, chosen to cover the shapes the resize and thumbnail
logic branches on. The first three have their dimensions and formats asserted
in `TestTestdataFixtures`, so a fixture that drifts fails loudly rather than
silently changing what the other tests mean.

| File                  | Size  | Format | Content                                   |
| --------------------- | ----- | ------ | ----------------------------------------- |
| `gradient_64x48.png`  | 64x48 | PNG    | Landscape RGB gradient                    |
| `checker_48x64.jpg`   | 48x64 | JPEG   | Portrait checkerboard, quality 90         |
| `alpha_32x32.png`     | 32x32 | PNG    | Square, opaque centre with a clear border |
| `exif_gps_48x64.jpg`  | 48x64 | JPEG   | The checkerboard, plus an EXIF block      |

They are generated, not photographic: every pixel comes from a formula, so they
compress to a few hundred bytes each and carry no licensing.

`exif_gps_48x64.jpg` is the one exception to carrying no metadata, and that is
the point of it. It holds a real EXIF block with GPS coordinates and a `Make`
of `IMAGEFORGE-GPS-CANARY`, so `TestStripMetadataRemovesTheCameraEXIF` can
check that the coordinates do not survive a transformation that was asked to
strip them. The coordinates are the Eiffel Tower's, not anyone's.
