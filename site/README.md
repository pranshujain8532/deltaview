# DeltaView website

The interactive walk-through published at https://pranshujain8532.github.io/deltaview/.

```sh
cd site
npm install
npm run dev        # http://localhost:5173
npm run build      # static files in dist/
```

Two pages: `index.html` (the walk-through) and `console.html` (operate a real engine).

The site runs in two modes. Without an engine, the playground, hero animation and Live section
run `src/lib/ivm.ts`, a small port of the engine's delta rules. With an engine, everything live
switches to the engine's real API (`src/lib/engine.ts`): its views, server-sent deltas, `/status`,
`/metrics` and, if started with `--sql-console`, `/sql`.

- Served by the engine (`http://localhost:8080`): connects automatically, same origin.
- From GitHub Pages: press the engine pill (top right) and connect to `http://localhost:8080`,
  or open the link the engine prints, which carries the console token in the URL fragment.

`make site` rebuilds the site and copies it into `internal/serve/web`, which the engine embeds.

Built with React, Vite, Tailwind CSS, Motion and GSAP. Components adapted from
[React Bits](https://reactbits.dev) (Threads, BlurText, ScrollReveal, ScrollVelocity, CountUp,
Magnet, SpotlightCard) and [Magic UI](https://magicui.design) (AnimatedBeam, Marquee, Terminal),
under their licenses. The DeltaFlow canvas animation is original.
