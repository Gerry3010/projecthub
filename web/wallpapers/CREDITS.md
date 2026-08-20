# Wallpaper-Presets — Herkunft & Lizenzen

Die Presets sind **selbst erzeugte Bilder**: generiert am 20.08.2026 über ElevenLabs
(Modell **GPT Image 2**, 16:9, 2K, Qualität „Medium") aus eigenen Prompts. Die Ausgaben
gehören dem erzeugenden Account; das Bündeln in diesem AGPL-lizenzierten Repository ist
damit unproblematisch. Kein Bild enthält Text, Logos, Personen oder Marken.

Bilder auf max. 1920 px (Längsseite) skaliert, Metadaten entfernt, JPEG (Qualität 82);
Thumbnails unter `thumbs/` (max. 480 px, Qualität 78).

Gestalterische Vorgabe für alle Prompts: **sehr dunkel, gedämpft, ruhige Mitte** — der
Hintergrund soll hinter den Tiles liegen, nicht mit ihnen konkurrieren.

| Datei | Motiv | Prompt (gekürzt) |
|-------|-------|------------------|
| `abstract-aurora-1/2.jpg` | Nordlicht-Bänder | flowing ribbons of light, deep indigo and teal on a near-black ground, aurora seen through frosted glass |
| `abstract-obsidian-1/2.jpg` | Obsidian | macro of black volcanic obsidian glass, faint iridescent oil-slick sheen, single cold rim light |
| `space-nebula-1/2.jpg` | Stiller Nebel | quiet deep-space nebula, dim dusty magenta and cyan at the edges, vast black centre, no bright core |
| `space-orbit.jpg` | Orbit-Sonnenaufgang | thin curved limb of a dark planet, band of blue airglow, black space, one soft sunrise glow |
| `nature-dusklake-1/2.jpg` | See in der Dämmerung | still alpine lake at dusk, deep teal water reflecting near-black pine forest, low cloud between the slopes |
| `nature-ridges-1/2.jpg` | Nebelgrate | layered mountain ridges fading into fog at blue hour, cold blue-grey monochrome |
| `code-circuit-1/2.jpg` | Platine | extreme macro of a dark circuit board at night, copper traces in faint warm amber, tiny bokeh lights |
| `code-terminal-1/2.jpg` | Terminalregen | vertical streaks of phosphor green and cold teal, heavy bokeh so no characters are readable |

## Verbliebenes Fremdmaterial

| Datei | Motiv | Lizenz | Quelle |
|-------|-------|--------|--------|
| `nature-lake.jpg` | Bergsee (türkis) | CC0 | Wikimedia Commons — Turquoise mountain lake (Unsplash) |

Die früheren NASA- und Commons-Presets (Carina-Nebel, Säulen der Schöpfung, Webb Deep
Field, Erde bei Nacht, Polarlicht, Berge von oben, Namib-Wüste, Nebelwald, Herbstwald,
Grünes Tal, Great Barrier Reef, Sunglint, Stadt bei Nacht) wurden am 20.08.2026 entfernt.
Wer noch auf eines davon zeigt, landet über `retiredPresets` (`internal/webui/wallpapers.go`)
auf dem nächstgelegenen neuen Preset.
