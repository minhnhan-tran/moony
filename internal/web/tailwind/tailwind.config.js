/** @type {import('tailwindcss').Config} */
// Tailwind config used to PREBUILD internal/web/static/tailwind.css. The Apple
// network blocks the Tailwind Play CDN, so the stylesheet is generated offline
// and vendored. Regenerate with `make tailwind` after changing utility classes
// in internal/web/static/index.html or any of the static/*.js modules.
//
// Colors are bound to CSS custom properties (the `--c-*` palette tokens defined
// in app.css) so the same utilities serve both the light and dark themes — the
// theme is switched by toggling `html.light` / `html.dark`, which swaps the
// token values. The `<alpha-value>` placeholder keeps Tailwind's opacity
// modifiers (e.g. `bg-base/50`) working. NOTE: app.css also re-maps these exact
// utilities to the same tokens, so theming works even against the currently
// vendored tailwind.css without a rebuild; keep the two in sync.
module.exports = {
  // Glob every markup/script source, not a fixed list: the SPA's single app.js was
  // long ago split into app-core/app-render/app-state/app-manage/app-workflow, so a
  // literal "app.js" entry silently matched NOTHING and every utility used only in JS
  // (w-full, font-medium, mt-3, focus:ring-1, …) was missing from the generated
  // stylesheet — the classes were in the markup but had no rule, so they did nothing.
  content: [
    "./internal/web/static/*.html",
    "./internal/web/static/*.js",
  ],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        base: "rgb(var(--c-base) / <alpha-value>)",
        surface: "rgb(var(--c-surface) / <alpha-value>)",
        raised: "rgb(var(--c-raised) / <alpha-value>)",
        edge: "rgb(var(--c-edge) / <alpha-value>)",
        ink: "rgb(var(--c-ink) / <alpha-value>)",
        faint: "rgb(var(--c-faint) / <alpha-value>)",
        brand: "rgb(var(--c-brand) / <alpha-value>)",
        // Input/well background behind text fields (bg-backdrop).
        backdrop: "rgb(var(--c-backdrop) / <alpha-value>)",
        ok: "rgb(var(--c-ok) / <alpha-value>)",
        warn: "rgb(var(--c-warn) / <alpha-value>)",
        danger: "rgb(var(--c-danger) / <alpha-value>)",
      },
      fontFamily: {
        sans: ["-apple-system", "BlinkMacSystemFont", "Segoe UI", "Roboto", "Oxygen", "Ubuntu", "Cantarell", "Fira Sans", "Droid Sans", "Helvetica Neue", "sans-serif"],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
    },
  },
  plugins: [],
};
