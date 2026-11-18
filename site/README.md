# DeltaView website

The interactive walk-through published at https://pranshujain8532.github.io/deltaview/.

```sh
cd site
npm install
npm run dev        # http://localhost:5173
npm run build      # static files in dist/
```

The playground and the in-browser simulation run `src/lib/ivm.ts`, a small port of the
engine's delta rules (filter, join with arrangements, group aggregate). The Live section
can also follow a real engine: start `deltaview` locally and press Connect.

Built with React, Vite, Tailwind CSS and Motion. Components adapted from
[React Bits](https://reactbits.dev) (BlurText, CountUp, DecryptedText, DotGrid, GlareHover,
LogoLoop, Magnet, SpotlightCard) and [Magic UI](https://magicui.design) (AnimatedBeam,
BorderBeam, Marquee, NumberTicker, Terminal), under their licenses.
