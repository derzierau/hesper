# Hesper site

The landing page: Next.js (App Router, static export), TypeScript, CSS Modules. No UI framework, no Tailwind.

- `npm install && npm run dev` to work on it, `npm run build` writes `out/`.
- Tokens live in `app/globals.css` and are copied from `app/Sources/Hesper/UI/Theme.swift`. No literal colors anywhere else.
- Primitives in `components/ui` mirror the app's (`docs/design-system.md`): StateMark (squares, never dots), Pill, Kbd, Tile.
- Signal pink is only for "needs you". Working indigo is the accent. Weights 400/500/600 only. Radii 4/7/10/14. Motion 120/180/220 ms on one curve, none under reduced motion.
- Every number and claim on the page must exist in `README.md` or `docs/`. `lib/content.ts` holds the shared ones.
- Never ship real session data; all terminal content is fixture text.
- Brand files: `public/mark.svg` (vector icon), `app/icon.svg` (favicon), `brand/og.html` rendered to `public/og.png` by `scripts/og.sh` (needs Google Chrome). Re-run the script after changing the headline.
- `SITE_URL` sets the canonical origin for Open Graph; `BASE_PATH` sets the subpath for GitHub Pages.

## Motion
- One orchestrated moment per section, started by `useInView` from `lib/motion.ts`, never on scroll position, never replayed. Everything else answers a visitor's action (a key, a click).
- Animate only `transform` and `opacity`, plus text that appears line by line. Durations from the tokens: `--m-quick`, `--m-move`, `--m-settle`, `--m-type`, `--m-stagger`, `--m-travel`. One curve, `--ease`. No springs, no bounces.
- `useReducedMotion` renders the finished state immediately. Keyboard shortcuts on the page (⌘J, ⏎, ⌘Z) are a demo of the app's keys and never conflict with the browser's.
- No uppercase eyebrow labels, no meta strings joined with middle dots, no accent color on one word of a headline. Section headings carry the meaning alone.

## Deploy
The site runs as a small nginx container next to a relay's shared nginx + certbot stack (`relay/docs/deployment.md`, option B). The shared nginx picks its vhost by SNI and forwards to the container; the stack's certbot renews the certificate.

```sh
export SITE_ENV_FILE=../relay/deploy/local.env   # the relay's server and proxy names
scripts/deploy.sh site   # build for https://$SITE_DOMAIN, upload, (re)start the container
scripts/deploy.sh acme   # once: HTTP-only vhost for the first certificate
scripts/deploy.sh tls    # once DNS points at the server: certificate, then the HTTPS vhost
```

`SITE_DOMAIN` defaults to `hesper.wtf`. Point its A record, and `www` (A or CNAME), at the server before `tls`.
