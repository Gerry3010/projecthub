# Wallpaper-Presets — Quellen & Lizenzen

Alle gebündelten Hintergrundbilder stammen aus der **NASA Image and Video Library**
(<https://images.nasa.gov>). NASA-Bildmaterial ist grundsätzlich **gemeinfrei / Public
Domain** und darf ohne Einschränkung verwendet werden (siehe NASA Media Usage
Guidelines, <https://www.nasa.gov/nasa-brand-center/images-and-media/>). Damit ist das
Bündeln in diesem AGPL-lizenzierten Repository unproblematisch.

Bilder wurden auf max. 1920 px (Längsseite) skaliert, Metadaten entfernt und als JPEG
(Qualität 82) gespeichert; Thumbnails unter `thumbs/` (max. 480 px, Qualität 78).

| Datei | Motiv | NASA-Asset | Instrument / Quelle |
|-------|-------|------------|---------------------|
| `space-carina.jpg` | Carina-Nebel („Cosmic Cliffs") | `carina_nebula` | JWST (NASA/ESA/CSA/STScI) |
| `space-pillars.jpg` | Säulen der Schöpfung | `PIA25433` | JWST (NASA/ESA/CSA/STScI) |
| `space-deepfield.jpg` | Webb's First Deep Field (SMACS 0723) | `webb_first_deep_field` | JWST (NASA/ESA/CSA/STScI) |
| `science-earthnight.jpg` | Erde bei Nacht („Black Marble") | `GSFC_20171208_Archive_e002131` | Suomi NPP / VIIRS (NASA GSFC) |
| `science-aurora.jpg` | Polarlicht von der ISS | `iss072e159172` | ISS Expedition 72 (NASA) |
| `nature-mountains.jpg` | Bergkette aus dem Orbit | `iss072e398041` | ISS Expedition 72 (NASA) |
| `nature-dunes.jpg` | Namib-Wüste, Namibia | `PIA17632` | NASA/JPL |
| `sea-reef.jpg` | Great Barrier Reef, Australien | `sts048-151-250` | Space Shuttle STS-48 (NASA) |
| `sea-sunglint.jpg` | Ozean im Sonnenlicht (Sunglint) | `iss022e024557` | ISS Expedition 22 (NASA) |

Asset-URL-Schema: `https://images-assets.nasa.gov/image/<ASSET>/<ASSET>~large.jpg`.

## Weitere Presets — Wikimedia Commons (CC0 / Public Domain)

Ergänzt über die Wikimedia-Commons-API, hart auf **CC0** bzw. **Public Domain** gefiltert
(Lizenz aus `extmetadata`). Ebenfalls bundling-sicher unter der AGPL des Repos.

| Datei | Motiv | Lizenz | Commons-Datei |
|-------|-------|--------|---------------|
| `nature-forest.jpg` | Nebelwald | CC0 | Misty Forest (227339925) |
| `nature-lake.jpg` | Bergsee (türkis) | CC0 | Turquoise mountain lake (Unsplash) |
| `nature-autumn.jpg` | Herbstwald | Public Domain | Fall Colors 2019 (USFS, 20191010-FS-R9RO-GER-006) |
| `nature-valley.jpg` | Grünes Tal | Public Domain | Green Mountain Campground (48104515632) |
| `city-night.jpg` | Stadt bei Nacht | CC0 | Dubai skyscrapers at night 2011 |

Quelle jeweils `https://commons.wikimedia.org/wiki/File:<Commons-Datei>`.

Neue Presets: Bild herunterladen, auf ~2560 px skalieren (`magick <src> -resize
'2560x2560>' -strip -quality 82 <out>.jpg`), Thumbnail erzeugen, hier dokumentieren
und in `internal/webui/wallpapers.go` eintragen.
