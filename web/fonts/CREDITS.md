# Gebündelte Schriften — Quellen & Lizenzen

Alle Schriften sind variable woff2 und werden per `@font-face` in `web/theme.css`
eingebunden (same-origin, `/web/fonts/`).

| Datei(en) | Familie | Rolle | Quelle / Lizenz |
|-----------|---------|-------|-----------------|
| `space-grotesk-*` | Space Grotesk | `--font-display` | Fontsource (SIL OFL 1.1) |
| `geist-*` | Geist | `--font-ui` | Fontsource (SIL OFL 1.1) |
| `jetbrains-mono-*` | JetBrains Mono | `--font-mono` | Fontsource (SIL OFL 1.1) |
| `nerd-symbols.woff2` | PH Nerd Symbols | Terminal-Icon-Fallback | Subset aus Hack Nerd Font Mono |

## nerd-symbols.woff2

Per-Glyph-Fallback, damit Terminal-Prompts (Starship, Powerline-Segmente, Git-/Datei-
Icons) statt „Tofu"-Kästchen echte Glyphen zeigen. Enthält **nur** die Symbol-/PUA-
Bereiche (kein Latin — das kommt weiter vorne aus JetBrains Mono):

```
pyftsubset HackNerdFontMono-Regular.ttf \
  --unicodes="2500-25FF,2665,2666,26A1,2B58,E000-F8FF,F0000-FFFFD" \
  --flavor=woff2 --output-file=nerd-symbols.woff2
```

Basis: **Hack** (MIT) gepatcht via **Nerd Fonts** (ryanoasis/nerd-fonts). Die Icon-Sets
tragen ihre jeweiligen Lizenzen (überwiegend SIL OFL / MIT / CC), siehe das Nerd-Fonts-
Repository. Font-Stack im Terminal: `"JetBrains Mono", "PH Nerd Symbols", ui-monospace, …`.
